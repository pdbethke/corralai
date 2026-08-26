// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// TestParseLocalEndpoints covers the flag that places a LOCAL seat on a
// specific ollama daemon — the mechanism for putting two models on two GPUs,
// since a daemon is pinned to a device (HIP_VISIBLE_DEVICES) and corral
// chooses the daemon, never the device.
func TestParseLocalEndpoints(t *testing.T) {
	got, err := parseLocalEndpoints([]string{
		"mutant-generator=http://localhost:11435",
		"test-writer=http://localhost:11436",
	})
	if err != nil {
		t.Fatalf("parseLocalEndpoints: %v", err)
	}
	if got["mutant-generator"] != "http://localhost:11435" {
		t.Errorf("generator endpoint = %q", got["mutant-generator"])
	}
	if got["test-writer"] != "http://localhost:11436" {
		t.Errorf("writer endpoint = %q", got["test-writer"])
	}
}

// An unknown role must be REFUSED, not ignored. A typo that silently places a
// seat nowhere is the silently-discarded-input shape this codebase keeps
// producing — and here it would look like the seat ran on the intended card.
func TestParseLocalEndpointsRefusesUnknownRole(t *testing.T) {
	for _, bad := range []string{
		"writer=http://localhost:11435", // near-miss for test-writer
		"mutant_generator=http://localhost:11435",
		"=http://localhost:11435",
	} {
		if _, err := parseLocalEndpoints([]string{bad}); err == nil {
			t.Errorf("parseLocalEndpoints(%q) = nil error, want a refusal", bad)
		}
	}
}

// Malformed or non-absolute URLs are refused up front, not discovered as a
// connection error mid-run after jails and stores are already open.
func TestParseLocalEndpointsRefusesBadURL(t *testing.T) {
	for _, bad := range []string{
		"test-writer=",
		"test-writer=localhost:11436", // no scheme
		"test-writer=://nope",
		"test-writer", // no '='
	} {
		if _, err := parseLocalEndpoints([]string{bad}); err == nil {
			t.Errorf("parseLocalEndpoints(%q) = nil error, want a refusal", bad)
		}
	}
}

// The same role twice is a contradiction the operator must resolve.
func TestParseLocalEndpointsRefusesDuplicateRole(t *testing.T) {
	_, err := parseLocalEndpoints([]string{
		"test-writer=http://localhost:11435",
		"test-writer=http://localhost:11436",
	})
	if err == nil {
		t.Fatal("duplicate role = nil error, want a refusal")
	}
}

// TestLocalEndpointRoutesSeatToItsDaemon proves the placement actually takes
// effect: the seat's request must arrive at ITS daemon, not the process-wide
// OLLAMA_URL. This is the property the two-GPU setup depends on — two daemons,
// one per card — so a test that only checked the map would prove nothing.
func TestLocalEndpointRoutesSeatToItsDaemon(t *testing.T) {
	var writerHits, baseHits int32
	reply := `{"message":{"role":"assistant","content":"ok"},"done":true}`
	writerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&writerHits, 1)
		_, _ = io.WriteString(w, reply)
	}))
	defer writerSrv.Close()
	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&baseHits, 1)
		_, _ = io.WriteString(w, reply)
	}))
	defer baseSrv.Close()

	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("OLLAMA_URL", baseSrv.URL)

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "qwen3.5:9b-q8_0",
		advpool.RoleTestWriter:      "gemma4:12b",
	}
	chatterFor, err := localChatterFor(assign, nil, map[string]string{
		advpool.RoleTestWriter: writerSrv.URL,
	})
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}

	msg := []agentworker.Message{{Role: "user", Content: "hi"}}
	if _, err := chatterFor(advpool.RoleTestWriter).Chat(msg, nil); err != nil {
		t.Fatalf("writer chat: %v", err)
	}
	if _, err := chatterFor(advpool.RoleMutantGenerator).Chat(msg, nil); err != nil {
		t.Fatalf("generator chat: %v", err)
	}

	if got := atomic.LoadInt32(&writerHits); got != 1 {
		t.Errorf("placed seat reached its daemon %d time(s), want 1", got)
	}
	if got := atomic.LoadInt32(&baseHits); got != 1 {
		t.Errorf("unplaced seat reached OLLAMA_URL %d time(s), want 1", got)
	}
}

// An endpoint on a CLOUD seat is refused, never silently ignored: a dropped
// placement looks identical to a seat that ran where it was told.
func TestLocalEndpointRefusedForCloudSeat(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("GEMINI_API_KEY", "k")
	_, err := localChatterFor(
		advpool.RoleAssignment{advpool.RoleTestWriter: "gemini-3.7-flash"},
		nil,
		map[string]string{advpool.RoleTestWriter: "http://localhost:11436"},
	)
	if err == nil {
		t.Fatal("endpoint on a cloud seat = nil error, want a refusal")
	}
}
