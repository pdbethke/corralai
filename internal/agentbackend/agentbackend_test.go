// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBackendReports404AsUnreachable verifies that HTTP 404 from any backend
// yields an error where errors.Is(err, ErrModelUnreachable) is true, and that
// non-404 HTTP errors (e.g. 500) are NOT classified as unreachable.
func TestBackendReports404AsUnreachable(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		status := status
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"test"}`))
			}))
			defer srv.Close()

			// Exercise all three backends through postJSON (they all funnel through it).
			tests := []struct {
				name    string
				backend Backend
			}{
				{"ollama", &ollamaBackend{url: srv.URL, model: "test-model"}},
				{"openai", &openaiBackend{base: srv.URL, model: "test-model"}},
				{"anthropic", &anthropicBackend{base: srv.URL, model: "test-model"}},
			}
			for _, tc := range tests {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					_, err := tc.backend.Chat([]Message{{Role: "user", Content: "hello"}}, nil)
					if err == nil {
						t.Fatalf("%s: want error for HTTP %d, got nil", tc.name, status)
					}
					isUnreachable := errors.Is(err, ErrModelUnreachable)
					if status == http.StatusNotFound && !isUnreachable {
						t.Errorf("%s: HTTP 404 must classify as ErrModelUnreachable, got %v", tc.name, err)
					}
					if status != http.StatusNotFound && isUnreachable {
						t.Errorf("%s: HTTP %d must NOT classify as ErrModelUnreachable, got %v", tc.name, status, err)
					}
				})
			}
		})
	}
}

// TestBackendConnectionRefusedIsUnreachable verifies that a backend pointed at a
// closed port (connection refused) also returns ErrModelUnreachable.
func TestBackendConnectionRefusedIsUnreachable(t *testing.T) {
	// Bind to an ephemeral port then immediately close — guaranteed connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // close right away so the port is unreachable

	b := &ollamaBackend{url: addr, model: "test-model"}
	_, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("want error for connection-refused, got nil")
	}
	if !errors.Is(err, ErrModelUnreachable) {
		t.Errorf("connection-refused must classify as ErrModelUnreachable, got %v", err)
	}
}

func TestLLMHTTPTimeout(t *testing.T) {
	t.Setenv("AGENT_LLM_TIMEOUT_SECONDS", "")
	if got := llmHTTPTimeout(); got != 180*time.Second {
		t.Errorf("unset default = %v, want 180s", got)
	}
	t.Setenv("AGENT_LLM_TIMEOUT_SECONDS", "600")
	if got := llmHTTPTimeout(); got != 600*time.Second {
		t.Errorf("override = %v, want 600s", got)
	}
	for _, bad := range []string{"-5", "0", "abc", "  "} {
		t.Setenv("AGENT_LLM_TIMEOUT_SECONDS", bad)
		if got := llmHTTPTimeout(); got != 180*time.Second {
			t.Errorf("invalid %q = %v, want default 180s", bad, got)
		}
	}
}
