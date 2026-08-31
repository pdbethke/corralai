// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"context"
	"errors"
	"testing"
)

// TestFakeLoggerRecordsExactBytes pins the seam certify_repo.go's
// --transparency upload tests against: FakeLogger hands back a configured
// Entry (or error) and records exactly what it was given, byte for byte —
// no re-serialization, no copying that could mask a slice-aliasing bug.
func TestFakeLoggerRecordsExactBytes(t *testing.T) {
	f := &FakeLogger{Entry: LogEntry{LogIndex: 42, UUID: "abc-123", IntegratedTime: 1700000000}}
	envelope := []byte(`{"payload":"eyJmb28iOiJiYXIifQ==","payloadType":"application/vnd.in-toto+json","signatures":[]}`)
	pubKey := []byte("-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----\n")

	got, err := f.Upload(context.Background(), envelope, pubKey)
	if err != nil {
		t.Fatalf("Upload: unexpected error: %v", err)
	}
	if got != f.Entry {
		t.Fatalf("Upload returned %+v, want %+v", got, f.Entry)
	}
	if len(f.Uploads) != 1 || string(f.Uploads[0]) != string(envelope) {
		t.Fatalf("FakeLogger recorded %q, want the exact envelope bytes %q", f.Uploads, envelope)
	}
	if len(f.PubKeys) != 1 || string(f.PubKeys[0]) != string(pubKey) {
		t.Fatalf("FakeLogger recorded pubkey %q, want %q", f.PubKeys, pubKey)
	}
}

// TestFakeLoggerFailsOpen is the other half of the contract: a FakeLogger
// configured with an error hands that error straight back, still having
// recorded what it was given — so a caller's fail-open path (stderr line,
// NULL columns, exit code untouched) can be exercised deterministically.
func TestFakeLoggerFailsOpen(t *testing.T) {
	wantErr := errors.New("rekor: 503 service unavailable")
	f := &FakeLogger{Err: wantErr}

	_, err := f.Upload(context.Background(), []byte("envelope"), nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Upload error = %v, want %v", err, wantErr)
	}
	if len(f.Uploads) != 1 {
		t.Fatalf("FakeLogger did not record the failed attempt's bytes")
	}
}

// TestNewRekorDefaultsBaseURL pins the documented default endpoint for an
// empty baseURL, so a caller that passes "" (rather than spelling out
// https://rekor.sigstore.dev itself) gets the public instance, not a broken
// client pointed at nothing.
func TestNewRekorDefaultsBaseURL(t *testing.T) {
	l := NewRekor("")
	rl, ok := l.(*rekorLogger)
	if !ok {
		t.Fatalf("NewRekor(\"\") returned %T, want *rekorLogger", l)
	}
	if rl.baseURL != "https://rekor.sigstore.dev" {
		t.Fatalf("NewRekor(\"\").baseURL = %q, want the default public instance", rl.baseURL)
	}
}

// TestFakeLoggerGetRecordsTheRequestedIndex pins the Get half of the
// hermetic seam `corral verify` tests against: FakeLogger.Get hands back a
// configured entry (or error) and records every index it was asked for, in
// order — no network, ever.
func TestFakeLoggerGetRecordsTheRequestedIndex(t *testing.T) {
	f := &FakeLogger{GetEntry: LogEntry{LogIndex: 55, UUID: "uuid-xyz", IntegratedTime: 42, EnvelopeSHA256: "deadbeef"}}

	got, err := f.Get(context.Background(), 55)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got != f.GetEntry {
		t.Fatalf("Get returned %+v, want %+v", got, f.GetEntry)
	}
	if len(f.GetCalls) != 1 || f.GetCalls[0] != 55 {
		t.Fatalf("FakeLogger recorded %v, want [55]", f.GetCalls)
	}
}

// TestFakeLoggerGetFailsOpen mirrors Upload's fail-open contract: a
// configured GetErr is returned as-is, and the attempted index is still
// recorded.
func TestFakeLoggerGetFailsOpen(t *testing.T) {
	wantErr := errors.New("rekor: entry not found")
	f := &FakeLogger{GetErr: wantErr}

	_, err := f.Get(context.Background(), 12)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Get error = %v, want %v", err, wantErr)
	}
	if len(f.GetCalls) != 1 || f.GetCalls[0] != 12 {
		t.Fatalf("FakeLogger did not record the failed Get's index")
	}
}
