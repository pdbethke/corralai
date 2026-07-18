// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// TestTelemetrySignerEmitsBuildCertified is the regression test for the
// telemetry gap this fix closes: relocating the pool's signer into the leaf
// advpool package (CertSigner) dropped the brain's "build_certified"
// telemetry event on every adversarial-pool signing. telemetrySigner wraps
// CertSigner to restore it; this drives a REAL sign (real key, real
// buildstore, real telemetry store) and asserts the event lands with the
// same fields certifyBuild's own rec() call used to produce.
func TestTelemetrySignerEmitsBuildCertified(t *testing.T) {
	dir := t.TempDir()

	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	tel, err := telemetry.Open(filepath.Join(dir, "telemetry.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	signer := telemetrySigner{
		inner: advpool.CertSigner{Key: priv, Store: bs},
		tel:   tel,
		store: bs,
	}

	v := advpool.Verdict{
		Repo:         "example/repo",
		Commit:       "deadbeef01",
		Status:       advpool.StatusCertified,
		ModelsByRole: map[string]string{"test-writer": "qwen2.5-coder:7b"},
	}

	id, head, err := signer.SignVerdict(context.Background(), v)
	if err != nil {
		t.Fatalf("SignVerdict: %v", err)
	}
	if id == 0 || head == "" {
		t.Fatalf("SignVerdict returned zero id/head: id=%d head=%q", id, head)
	}

	n, err := tel.CountKind(0, "build_certified")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("build_certified events for mission 0 = %d, want 1", n)
	}

	events, err := tel.EventsForMission(0)
	if err != nil {
		t.Fatal(err)
	}
	var found *telemetry.Event
	for i := range events {
		if events[i].Kind == "build_certified" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no build_certified event recorded")
	}
	if found.Actor != "corral-advpool" {
		t.Errorf("actor = %q, want %q", found.Actor, "corral-advpool")
	}
	if found.Subject != "example/repo@deadbeef01" {
		t.Errorf("subject = %q, want %q", found.Subject, "example/repo@deadbeef01")
	}
	if got := found.Detail["repo"]; got != "example/repo" {
		t.Errorf("detail[repo] = %v, want %q", got, "example/repo")
	}
	if got := found.Detail["commit"]; got != "deadbeef01" {
		t.Errorf("detail[commit] = %v, want %q", got, "deadbeef01")
	}
	if got := found.Detail["head"]; got != head {
		t.Errorf("detail[head] = %v, want %q", got, head)
	}
	if got, ok := found.Detail["anchored"].(bool); !ok || got != false {
		t.Errorf("detail[anchored] = %v, want false (no witness configured)", found.Detail["anchored"])
	}
}

// TestTelemetrySignerSkipsEventOnSignError asserts telemetrySigner does not
// emit a "build_certified" event when the inner sign fails — the wrapper
// must not fabricate a certified-event for a run that never actually got a
// signed record.
func TestTelemetrySignerSkipsEventOnSignError(t *testing.T) {
	dir := t.TempDir()

	tel, err := telemetry.Open(filepath.Join(dir, "telemetry.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()

	// A CertSigner with a nil Key makes ed25519 signing fail, so
	// inner.SignVerdict returns a non-nil error before ever calling Store.Save.
	signer := telemetrySigner{
		inner: advpool.CertSigner{Key: nil, Store: nil},
		tel:   tel,
		store: nil,
	}

	_, _, err = signer.SignVerdict(context.Background(), advpool.Verdict{Repo: "r", Commit: "c"})
	if err == nil {
		t.Fatal("expected SignVerdict to fail with a nil signing key")
	}

	n, err := tel.CountKind(0, "build_certified")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("build_certified events after a failed sign = %d, want 0", n)
	}
}
