// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// sha256Hex is the hex sha256 of b — the same formula Rekor's dsse entry
// type uses for spec.envelopeHash, so Upload's own EnvelopeSHA256 and Get's
// (read off the log) are directly comparable.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

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
	// EnvelopeSHA256 is the hex sha256 of the WHOLE DSSE envelope Rekor
	// recorded for this entry (its dsse v0.0.1 entry body carries this
	// under spec.envelopeHash — the hash of the entire envelope sent to
	// Rekor, not just the in-toto payload inside it). Get populates it from
	// the log's own record; Upload populates it from the envelope bytes it
	// was just handed, for symmetry. It is what `corral verify` compares
	// against a local envelope's own sha256 to confirm a claimed log entry
	// actually IS this file, and never anything else.
	EnvelopeSHA256 string
}

// Logger uploads an already-written attestation to a public transparency
// log. It is the seam `--transparency` tests against: every certify_repo.go
// test uses FakeLogger, never the network.
type Logger interface {
	// Upload submits envelope (read verbatim off disk — never
	// re-serialized) to the log, alongside the PEM-encoded public key a
	// verifier would need, and returns the log's receipt.
	Upload(ctx context.Context, envelope []byte, pubKeyPEM []byte) (LogEntry, error)
	// Get fetches the entry already logged at logIndex, for `corral verify`
	// to confirm an index a statement or the ledger names actually points
	// at a real entry — and, via EnvelopeSHA256, that the entry really is
	// the envelope on disk. This is Rekor's own record of the entry, read
	// straight back; it is NOT an offline Merkle-inclusion proof verified
	// against the Sigstore TUF trust root — that stronger, independent
	// check is witness.go's Witness.VerifyInclusion, a deliberately
	// separate integration (see doc.go). A caller that wants to disclose
	// the distinction should say so, not imply more than this checked.
	Get(ctx context.Context, logIndex int64) (LogEntry, error)
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
		EnvelopeSHA256: sha256Hex(envelope),
	}, nil
}

// dsseEntryBody is the JSON shape of a dsse v0.0.1 Rekor entry's body, once
// base64-decoded — specifically the one field this package needs:
// spec.envelopeHash, the hash Rekor computed over the WHOLE envelope it was
// handed at submission time (see
// github.com/sigstore/rekor/pkg/generated/models.DSSEV001Schema —
// deliberately NOT decoded via that generated, polymorphic-typed package
// here: its Spec field is `any`, and a small local struct matching Rekor's
// own documented dsse v0.0.1 schema is a clearer, more direct read of the
// one fact this file needs than round-tripping through that type's
// swagger-generated (un)marshaling).
type dsseEntryBody struct {
	Spec struct {
		EnvelopeHash struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"value"`
		} `json:"envelopeHash"`
	} `json:"spec"`
}

// Get fetches the entry at logIndex and returns its log coordinates plus
// the envelope hash Rekor itself recorded for it (spec.envelopeHash in the
// entry's own body) — see LogEntry.EnvelopeSHA256's doc for exactly what
// that does and does not prove.
func (r *rekorLogger) Get(ctx context.Context, logIndex int64) (LogEntry, error) {
	rc, err := client.GetRekorClient(r.baseURL)
	if err != nil {
		return LogEntry{}, fmt.Errorf("transparency: creating rekor client: %w", err)
	}

	params := entries.NewGetLogEntryByIndexParams().WithContext(ctx).WithLogIndex(logIndex)
	resp, err := rc.Entries.GetLogEntryByIndex(params)
	if err != nil {
		return LogEntry{}, fmt.Errorf("transparency: fetching entry at index %d: %w", logIndex, err)
	}

	var uuid string
	var found bool
	var le = struct {
		LogIndex       *int64
		IntegratedTime *int64
		Body           any
	}{}
	for u, entry := range resp.Payload {
		uuid = u
		le.LogIndex = entry.LogIndex
		le.IntegratedTime = entry.IntegratedTime
		le.Body = entry.Body
		found = true
		break
	}
	if !found {
		return LogEntry{}, fmt.Errorf("transparency: no entry found at index %d", logIndex)
	}
	if le.LogIndex == nil || le.IntegratedTime == nil {
		return LogEntry{}, errors.New("transparency: rekor entry missing index or integrated time")
	}

	bodyStr, ok := le.Body.(string)
	if !ok {
		return LogEntry{}, errors.New("transparency: rekor entry body is not a base64 string")
	}
	bodyBytes, err := base64.StdEncoding.DecodeString(bodyStr)
	if err != nil {
		return LogEntry{}, fmt.Errorf("transparency: decoding entry body: %w", err)
	}
	var parsed dsseEntryBody
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return LogEntry{}, fmt.Errorf("transparency: parsing entry body: %w", err)
	}

	return LogEntry{
		LogIndex:       *le.LogIndex,
		UUID:           uuid,
		IntegratedTime: *le.IntegratedTime,
		EnvelopeSHA256: parsed.Spec.EnvelopeHash.Value,
	}, nil
}

// FakeLogger is a hermetic, in-memory Logger for tests — no network, ever.
// It records every call it receives (the exact bytes given, in order) so a
// test can assert byte-identity between what was uploaded and the file on
// disk, and hands back either the configured Entry or the configured Err.
type FakeLogger struct {
	// Entry is returned by Upload on every call when Err is nil.
	Entry LogEntry
	// Err, when set, is returned by Upload on every call instead of Entry —
	// the fail-open path a caller must handle without changing its exit
	// code.
	Err error
	// Uploads and PubKeys record each Upload call's arguments, in order, so
	// a test can confirm the exact bytes an upload received.
	Uploads [][]byte
	PubKeys [][]byte

	// GetEntry is returned by Get on every call when GetErr is nil.
	GetEntry LogEntry
	// GetErr, when set, is returned by Get on every call instead of
	// GetEntry.
	GetErr error
	// GetCalls records every index Get was asked for, in order.
	GetCalls []int64
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

// Get records logIndex, then returns GetEntry or GetErr.
func (f *FakeLogger) Get(_ context.Context, logIndex int64) (LogEntry, error) {
	f.GetCalls = append(f.GetCalls, logIndex)
	if f.GetErr != nil {
		return LogEntry{}, f.GetErr
	}
	return f.GetEntry, nil
}
