// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/go-openapi/strfmt"
	"github.com/sigstore/rekor/pkg/client"
	genclient "github.com/sigstore/rekor/pkg/generated/client"
	"github.com/sigstore/rekor/pkg/generated/client/entries"
	"github.com/sigstore/rekor/pkg/generated/models"
	"github.com/sigstore/rekor/pkg/tle"
	rekortypes "github.com/sigstore/rekor/pkg/types"
	rekordsse "github.com/sigstore/rekor/pkg/types/dsse"
	// Registers the dsse v0.0.1 type so rekortypes can unmarshal/build it.
	_ "github.com/sigstore/rekor/pkg/types/dsse/v0.0.1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore/pkg/signature"
)

// RekorOption configures a RekorWitness.
type RekorOption func(*rekorWitness)

// WithSignerPublicKey supplies the Ed25519 public key that signed the DSSE
// envelopes this witness will anchor. Rekor's dsse entry type verifies an
// envelope's signature at submission time and stores the verifier alongside
// the entry — but a DSSE envelope does not carry its own public key, so the
// witness must be told it. In production the corral has one signing key; the
// caller passes its public half here. (Only the PUBLIC key is used; it is
// never logged.) Without it, Anchor returns an error rather than guessing.
func WithSignerPublicKey(pub ed25519.PublicKey) RekorOption {
	return func(w *rekorWitness) { w.signerPub = pub }
}

// rekorWitness is a thin wrapper over a Sigstore Rekor v1 instance. It submits
// dsse-type entries and verifies their inclusion proofs offline against the
// Rekor public key obtained from the Sigstore TUF trust root (fetched once at
// construction) — never from the entry itself or the instance's key endpoint.
//
// The Rekor/Sigstore client types are contained entirely within this file;
// only the package's own Entry crosses the boundary.
type rekorWitness struct {
	rekorURL  string
	signerPub ed25519.PublicKey
	// rekorLogs maps hex(logID) -> the TUF-rooted transparency-log public key
	// and validity window, used to verify inclusion proofs and SETs offline.
	rekorLogs map[string]*root.TransparencyLog
}

// NewRekorWitness returns a Witness backed by the Rekor instance at rekorURL
// (e.g. https://rekor.sigstore.dev). It fetches the Sigstore trusted root via
// TUF once, so subsequent VerifyInclusion calls are fully offline. Pass
// WithSignerPublicKey to enable Anchor.
func NewRekorWitness(rekorURL string, opts ...RekorOption) (Witness, error) {
	if rekorURL == "" {
		return nil, errors.New("transparency: rekor URL is required")
	}
	w := &rekorWitness{rekorURL: rekorURL}
	for _, o := range opts {
		o(w)
	}

	// Fetch Rekor's public key(s) from the Sigstore TUF trust root. This is
	// the trust anchor: we verify inclusion proofs and SETs against THIS key,
	// not against anything the Rekor instance hands back inline.
	tr, err := root.FetchTrustedRoot()
	if err != nil {
		return nil, fmt.Errorf("transparency: fetching Sigstore TUF trusted root: %w", err)
	}
	w.rekorLogs = tr.RekorLogs()
	if len(w.rekorLogs) == 0 {
		return nil, errors.New("transparency: TUF trusted root contained no Rekor log keys")
	}
	return w, nil
}

// Anchor submits dsseEnvelope to Rekor as a dsse-type entry and captures the
// resulting log index, log ID, integrated time, inclusion proof, SET, and
// canonicalized body.
func (w *rekorWitness) Anchor(ctx context.Context, dsseEnvelope []byte) (Entry, error) {
	if len(dsseEnvelope) == 0 {
		return Entry{}, errors.New("transparency: cannot anchor an empty envelope")
	}
	if len(w.signerPub) != ed25519.PublicKeySize {
		return Entry{}, errors.New("transparency: RekorWitness needs the signer's public key to anchor a dsse entry; construct it WithSignerPublicKey")
	}

	pubPEM, err := marshalPublicKeyPEM(w.signerPub)
	if err != nil {
		return Entry{}, err
	}

	// Build a dsse-type proposed entry. Rekor verifies the envelope's
	// signature against the supplied public key at insert time.
	dsseType := rekordsse.New()
	pe, err := dsseType.CreateProposedEntry(ctx, "", rekortypes.ArtifactProperties{
		ArtifactBytes:  dsseEnvelope,
		PublicKeyBytes: [][]byte{pubPEM},
	})
	if err != nil {
		return Entry{}, fmt.Errorf("transparency: building dsse proposed entry: %w", err)
	}

	rc, err := client.GetRekorClient(w.rekorURL)
	if err != nil {
		return Entry{}, fmt.Errorf("transparency: creating rekor client: %w", err)
	}

	params := entries.NewCreateLogEntryParams().WithContext(ctx)
	params.SetProposedEntry(pe)
	resp, err := rc.Entries.CreateLogEntry(params)
	if err != nil {
		return Entry{}, fmt.Errorf("transparency: submitting entry to rekor: %w", err)
	}

	logEntry, ok := resp.Payload[resp.ETag]
	if !ok {
		return Entry{}, errors.New("transparency: rekor response did not contain the created entry")
	}

	// The create response may omit the Merkle inclusion proof; re-fetch by
	// UUID until it is present, so VerifyInclusion has real proof material.
	if logEntry.Verification == nil || logEntry.Verification.InclusionProof == nil {
		fetched, ferr := w.fetchEntryByUUID(ctx, rc, resp.ETag)
		if ferr != nil {
			return Entry{}, ferr
		}
		logEntry = fetched
	}

	return w.toEntry(logEntry)
}

// fetchEntryByUUID retrieves a fully-populated log entry (with inclusion
// proof) by its UUID.
func (w *rekorWitness) fetchEntryByUUID(ctx context.Context, rc *genclient.Rekor, uuid string) (models.LogEntryAnon, error) {
	p := entries.NewGetLogEntryByUUIDParams().WithContext(ctx).WithEntryUUID(uuid)
	resp, err := rc.Entries.GetLogEntryByUUID(p)
	if err != nil {
		return models.LogEntryAnon{}, fmt.Errorf("transparency: fetching entry %s: %w", uuid, err)
	}
	le, ok := resp.Payload[uuid]
	if !ok {
		// The map may be keyed by the full entry ID; take the sole element.
		for _, v := range resp.Payload {
			le = v
			ok = true
			break
		}
	}
	if !ok {
		return models.LogEntryAnon{}, fmt.Errorf("transparency: entry %s not found on refetch", uuid)
	}
	return le, nil
}

// toEntry converts a Rekor LogEntryAnon into the package's transport-neutral
// Entry, keeping all Rekor types on this side of the boundary.
func (w *rekorWitness) toEntry(le models.LogEntryAnon) (Entry, error) {
	if le.LogIndex == nil || le.LogID == nil || le.IntegratedTime == nil {
		return Entry{}, errors.New("transparency: rekor entry missing index/logID/integratedTime")
	}
	if le.Verification == nil || le.Verification.InclusionProof == nil {
		return Entry{}, errors.New("transparency: rekor entry missing inclusion proof")
	}

	bodyStr, ok := le.Body.(string)
	if !ok {
		return Entry{}, errors.New("transparency: rekor entry body is not a base64 string")
	}
	bodyBytes, err := base64.StdEncoding.DecodeString(bodyStr)
	if err != nil {
		return Entry{}, fmt.Errorf("transparency: decoding entry body: %w", err)
	}

	proofBytes, err := json.Marshal(le.Verification.InclusionProof)
	if err != nil {
		return Entry{}, fmt.Errorf("transparency: serializing inclusion proof: %w", err)
	}

	return Entry{
		LogIndex:       *le.LogIndex,
		LogID:          *le.LogID,
		IntegratedTime: *le.IntegratedTime,
		InclusionProof: proofBytes,
		SET:            []byte(le.Verification.SignedEntryTimestamp),
		Body:           bodyBytes,
	}, nil
}

// VerifyInclusion verifies, entirely offline, that entry is a valid Rekor
// inclusion proof for dsseEnvelope:
//  1. the Merkle inclusion proof and its checkpoint signature verify under the
//     TUF-rooted Rekor public key,
//  2. the Signed Entry Timestamp verifies under that same key, and
//  3. the entry's stored DSSE payload hash equals sha256(the envelope payload).
//
// Any mismatch returns (false, reason). It never calls back to the Rekor
// instance, so a compromised instance cannot influence the result.
func (w *rekorWitness) VerifyInclusion(entry Entry, dsseEnvelope []byte) (bool, string) {
	logIDBytes, err := hex.DecodeString(entry.LogID)
	if err != nil {
		return false, "log ID is not valid hex"
	}

	var proof models.InclusionProof
	if err := json.Unmarshal(entry.InclusionProof, &proof); err != nil {
		return false, "inclusion proof is not well-formed"
	}

	// Reconstruct the log entry via the protobuf representation, which keeps
	// the GLOBAL log index (used by the SET) distinct from the IN-TREE
	// inclusion-proof index (used by the Merkle proof). Rekor is sharded, so
	// these differ; the deprecated tlog.NewEntry conflates them and must not
	// be used here.
	logIndex := entry.LogIndex
	integratedTime := entry.IntegratedTime
	logID := entry.LogID
	anon := models.LogEntryAnon{
		Body:           base64.StdEncoding.EncodeToString(entry.Body),
		IntegratedTime: &integratedTime,
		LogID:          &logID,
		LogIndex:       &logIndex,
		Verification: &models.LogEntryAnonVerification{
			InclusionProof:       &proof,
			SignedEntryTimestamp: strfmt.Base64(entry.SET),
		},
	}
	proto, err := tle.GenerateTransparencyLogEntry(anon)
	if err != nil {
		return false, fmt.Sprintf("reconstructing log entry: %v", err)
	}
	tlogEntry, err := tlog.NewTlogEntry(proto)
	if err != nil {
		return false, fmt.Sprintf("parsing log entry: %v", err)
	}

	// Look up the TUF-rooted verifier for this log.
	hexKey := hex.EncodeToString(logIDBytes)
	tlogVerifier, ok := w.rekorLogs[hexKey]
	if !ok {
		return false, "no TUF-rooted public key for this transparency log"
	}
	verifier, err := signature.LoadVerifier(tlogVerifier.PublicKey, tlogVerifier.SignatureHashFunc)
	if err != nil {
		return false, "loading transparency-log verifier failed"
	}

	// 1. Merkle inclusion proof + checkpoint signature.
	if err := tlog.VerifyInclusion(tlogEntry, verifier); err != nil {
		return false, fmt.Sprintf("inclusion proof did not verify: %v", err)
	}

	// 2. Signed Entry Timestamp.
	if len(entry.SET) > 0 {
		if err := tlog.VerifySET(tlogEntry, w.rekorLogs); err != nil {
			return false, fmt.Sprintf("signed entry timestamp did not verify: %v", err)
		}
	}

	// 3. Confirm the logged entry actually wraps THIS envelope by comparing
	// the entry's stored payload hash to sha256(the envelope's payload).
	entryDigest, ok := tlogEntry.GetDssePayloadHash()
	if !ok {
		return false, "log entry is not a dsse entry or lacks a payload hash"
	}
	envDigest, err := envelopePayloadSHA256(dsseEnvelope)
	if err != nil {
		return false, "given envelope is malformed"
	}
	if !bytesEqual(entryDigest, envDigest[:]) {
		return false, "log entry does not wrap the given envelope (payload hash mismatch)"
	}

	return true, "rekor inclusion proof and SET verified against the TUF trust root"
}

// envelopePayloadSHA256 decodes a DSSE envelope's base64 payload and returns
// its SHA-256, matching how Rekor's dsse type stores the payload hash.
func envelopePayloadSHA256(dsseEnvelope []byte) ([32]byte, error) {
	var env struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(dsseEnvelope, &env); err != nil {
		return [32]byte{}, err
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(env.Payload)
		if err != nil {
			return [32]byte{}, err
		}
	}
	return sha256.Sum256(payload), nil
}

// marshalPublicKeyPEM encodes an Ed25519 public key as a PKIX PEM block, the
// form Rekor's dsse type expects for a verifier.
func marshalPublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("transparency: marshaling public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}
