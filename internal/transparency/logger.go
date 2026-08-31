// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"context"
	"errors"
	"fmt"

	"github.com/sigstore/rekor/pkg/client"
	"github.com/sigstore/rekor/pkg/generated/client/entries"
	rekortypes "github.com/sigstore/rekor/pkg/types"
	rekordsse "github.com/sigstore/rekor/pkg/types/dsse"
	// Registers the dsse v0.0.1 entry type so rekortypes can build it —
	// mirrors the import rekor.go already carries for the Witness path.
	_ "github.com/sigstore/rekor/pkg/types/dsse/v0.0.1"
)

// LogEntry is the receipt for one envelope uploaded to a public transparency
// log: the log index and entry UUID a reader can hand to Rekor's own UI or
// API to look the entry back up, plus the time the log committed it.
//
// See doc.go for how this file's Logger/LogEntry relate to witness.go's
// Witness/Entry — two independent Rekor paths sharing this package, on
// purpose not unified.
type LogEntry struct {
	LogIndex       int64
	UUID           string
	IntegratedTime int64
}

// Logger uploads an already-written attestation to a public transparency
// log. It is the seam `--transparency` tests against: every certify_repo.go
// test uses FakeLogger, never the network.
type Logger interface {
	// Upload submits envelope (read verbatim off disk — never
	// re-serialized) to the log, alongside the PEM-encoded public key a
	// verifier would need, and returns the log's receipt.
	Upload(ctx context.Context, envelope []byte, pubKeyPEM []byte) (LogEntry, error)
}

// defaultRekorURL is the public Sigstore Rekor instance — the default
// NewRekor uses when the caller does not name one.
const defaultRekorURL = "https://rekor.sigstore.dev"

// rekorLogger is the real Logger, backed by a Sigstore Rekor v1 instance.
type rekorLogger struct{ baseURL string }

// NewRekor returns a Logger backed by the Rekor instance at baseURL
// ("" defaults to the public https://rekor.sigstore.dev). Construction does
// no network I/O — the client is only built, and the log only touched, on
// Upload — so calling this unconditionally costs nothing when --transparency
// was not given.
func NewRekor(baseURL string) Logger {
	if baseURL == "" {
		baseURL = defaultRekorURL
	}
	return &rekorLogger{baseURL: baseURL}
}

// Upload submits envelope to Rekor as a dsse-type entry and returns its log
// index, UUID and integrated time.
func (r *rekorLogger) Upload(ctx context.Context, envelope []byte, pubKeyPEM []byte) (LogEntry, error) {
	if len(envelope) == 0 {
		return LogEntry{}, errors.New("transparency: cannot upload an empty envelope")
	}

	dsseType := rekordsse.New()
	pe, err := dsseType.CreateProposedEntry(ctx, "", rekortypes.ArtifactProperties{
		ArtifactBytes:  envelope,
		PublicKeyBytes: [][]byte{pubKeyPEM},
	})
	if err != nil {
		return LogEntry{}, fmt.Errorf("transparency: building dsse proposed entry: %w", err)
	}

	rc, err := client.GetRekorClient(r.baseURL)
	if err != nil {
		return LogEntry{}, fmt.Errorf("transparency: creating rekor client: %w", err)
	}

	params := entries.NewCreateLogEntryParams().WithContext(ctx)
	params.SetProposedEntry(pe)
	resp, err := rc.Entries.CreateLogEntry(params)
	if err != nil {
		return LogEntry{}, fmt.Errorf("transparency: submitting entry to rekor: %w", err)
	}

	logEntry, ok := resp.Payload[resp.ETag]
	if !ok {
		return LogEntry{}, errors.New("transparency: rekor response did not contain the created entry")
	}
	if logEntry.LogIndex == nil || logEntry.IntegratedTime == nil {
		return LogEntry{}, errors.New("transparency: rekor entry missing log index or integrated time")
	}

	return LogEntry{
		LogIndex:       *logEntry.LogIndex,
		UUID:           resp.ETag,
		IntegratedTime: *logEntry.IntegratedTime,
	}, nil
}

// FakeLogger is a hermetic, in-memory Logger for tests — no network, ever.
// It records every call it receives (the exact bytes given, in order) so a
// test can assert byte-identity between what was uploaded and the file on
// disk, and hands back either the configured Entry or the configured Err.
type FakeLogger struct {
	// Entry is returned on every call when Err is nil.
	Entry LogEntry
	// Err, when set, is returned by every call instead of Entry — the
	// fail-open path a caller must handle without changing its exit code.
	Err error
	// Uploads and PubKeys record each call's arguments, in order, so a test
	// can confirm the exact bytes an upload received.
	Uploads [][]byte
	PubKeys [][]byte
}

// Upload records envelope and pubKeyPEM, then returns Entry or Err.
func (f *FakeLogger) Upload(_ context.Context, envelope []byte, pubKeyPEM []byte) (LogEntry, error) {
	f.Uploads = append(f.Uploads, append([]byte(nil), envelope...))
	f.PubKeys = append(f.PubKeys, append([]byte(nil), pubKeyPEM...))
	if f.Err != nil {
		return LogEntry{}, f.Err
	}
	return f.Entry, nil
}
