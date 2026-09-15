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
	"strings"

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
	//
	// Coupling note: this witness ALWAYS resolves keys from the PUBLIC
	// Sigstore TUF trust root, regardless of rekorURL — so it only verifies
	// entries anchored to the public Sigstore Rekor. Pointing rekorURL at a
	// non-public Rekor instance does not make this witness trust that
	// instance's key; VerifyInclusion will fail closed ("no TUF-rooted public
	// key for this transparency log") because a private log's logID/key isn't
	// in the public root. True air-gap / private-Rekor support needs a
	// custom-trust-root option and is v2, not this file today.
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
	if le, ok := resp.Payload[uuid]; ok {
		return le, nil
	}
	// The map may be keyed by the full entry ID rather than the UUID, so the
	// single element is taken — but ONLY when there is exactly one.
	_, le, err := soleEntry(resp.Payload)
	if err != nil {
		return models.LogEntryAnon{}, fmt.Errorf("transparency: refetching entry %s: %w", uuid, err)
	}
	return le, nil
}

// errSeveralEntries is returned by soleEntry when a response holds more than
// one entry and the caller has no key to pick by.
var errSeveralEntries = errors.New("rekor returned several entries — refusing to guess which is the one")

// soleEntry returns the ONE entry in a Rekor response, keyed as Rekor keyed
// it, or an error when the response holds none or several.
//
// This is the only place that takes an element out of a Rekor response map
// without a key. Ranging over the map and breaking took an ARBITRARY element
// in map order; that was found and fixed in fetchEntryByUUID (round three,
// R7) and then found again, unchanged, in the logger's Get twelve lines from
// code edited the same day (round four, R6). A second copy of the rule was
// the defect, so there is one function and both doors call it.
func soleEntry(payload models.LogEntry) (string, models.LogEntryAnon, error) {
	switch len(payload) {
	case 0:
		return "", models.LogEntryAnon{}, errors.New("rekor returned no entry")
	case 1:
		for u, v := range payload {
			return u, v, nil
		}
	}
	return "", models.LogEntryAnon{}, fmt.Errorf("%w (%d)", errSeveralEntries, len(payload))
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
	// The SET is required HERE, at the door that builds an Entry, for the
	// same reason VerifyInclusion requires it at the door that checks one:
	// without it the log index and integrated time are unauthenticated.
	// Requiring it only in the verifier let Anchor hand back an Entry that
	// its own verifier would refuse, and the caller learned that later, from
	// a less specific error. (Round four, 2026-09-13, R2 — pre-existing since
	// 2026-07-10.)
	if len(le.Verification.SignedEntryTimestamp) == 0 {
		return Entry{}, errors.New("transparency: rekor entry has no signed entry timestamp — its log index and integrated time would be unauthenticated, and VerifyInclusion refuses exactly that")
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
//  2. the Signed Entry Timestamp is PRESENT and verifies under that same key —
//     without it the global log index and the integrated time are
//     unauthenticated and freely editable, and the TUF key's validity window
//     is never checked,
//  3. the entry's stored DSSE payload hash equals sha256(the envelope payload),
//     and
//  4. every signature the envelope carries appears in the signature set the
//     entry recorded — because step 3 alone accepts a replacement envelope
//     signed by a different key over the same payload.
//
// Any mismatch returns (false, reason). It never calls back to the Rekor
// instance, so a compromised instance cannot influence the result.
//
// The one thing it does NOT promise: for an entry kind whose body shape this
// package cannot read, step 4 is skipped and the returned reason says so. The
// binding is then payload-only, which is weaker — never silently so.
func (w *rekorWitness) VerifyInclusion(entry Entry, dsseEnvelope []byte) (bool, string) {
	logIDBytes, err := hex.DecodeString(entry.LogID)
	if err != nil {
		return false, "log ID is not valid hex"
	}

	// A MISSING SIGNED ENTRY TIMESTAMP IS FATAL, and checked first.
	//
	// The Merkle proof covers the leaf and the IN-TREE index; it does not
	// cover the global LogIndex or IntegratedTime. Only the SET does. So while
	// the SET was optional, anyone holding a real record could delete it and
	// then edit both to anything, and `certify verify` printed the invented
	// values as "verified (publicly witnessed <time>, Rekor #N)". The TUF key
	// validity-window check lives only inside VerifySET and went with it.
	//
	// Requiring it rejects nothing legitimate — Rekor returns a SET on every
	// entry (this project's own, log index 2759598612, carries 96 bytes).
	// (Cold review round three, 2026-09-12, R1 — reproduced, high, and
	// pre-existing since 2026-07-10.)
	if len(entry.SET) == 0 {
		return false, "no signed entry timestamp: the log index and integrated time are unauthenticated, so this entry cannot be said to be publicly witnessed"
	}

	proof, err := parseInclusionProof(entry.InclusionProof)
	if err != nil {
		return false, err.Error()
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
			InclusionProof:       proof,
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

	// 2. Signed Entry Timestamp — REQUIRED, not "checked if present"
	//    (the presence half of this rule is enforced at the top of the
	//    function, so a missing SET is refused before any work and with a
	//    reason a reader can act on).
	//
	// Treating it as optional meant an attacker holding a real record could
	// DELETE the SET and then edit the entry's IntegratedTime and global
	// LogIndex to anything at all, because nothing else authenticates them:
	// the Merkle proof in step 1 covers the leaf and the in-tree index, not
	// the global index or the timestamp. `certify verify` then printed the
	// invented values as "verified (publicly witnessed <time>, Rekor #N)".
	// The TUF key's validity-window check lives only inside VerifySET, so
	// skipping the step skipped that too.
	//
	// Requiring it rejects nothing legitimate: Rekor returns a SET on every
	// entry (verified against this project's own entry, log index 2759598612,
	// where it is 96 bytes), and an entry without one was never witnessed in
	// the sense the CLI claims. (Cold review round three, 2026-09-12, R1 —
	// reproduced, high, and pre-existing since 2026-07-10.)
	if err := tlog.VerifySET(tlogEntry, w.rekorLogs); err != nil {
		return false, fmt.Sprintf("signed entry timestamp did not verify: %v", err)
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

	// 4. BIND THE ENTRY TO THIS EXACT ENVELOPE.
	//
	// The payload hash alone does not: a replacement envelope signed by a
	// different key over the same payload passes step 3. Two bindings are
	// tried, strongest first, and the failure modes are kept distinguishable.
	bind, reason := bindEntryToEnvelope(entry.Body, dsseEnvelope)
	switch bind {
	case bindMismatch:
		return false, "log entry does not wrap the given envelope (" + reason + ")"
	case bindMalformedEnvelope:
		// THE ENVELOPE IS ATTACKER INPUT. The previous version funnelled this
		// into the same "disclose, do not refuse" path as an unreadable LOG
		// body, so adding one non-string `sig` element — or sending no
		// signatures at all — turned a refused never-logged signature into
		// ok=true, while the reason string said "unrecognized entry body
		// shape" about a body that had read perfectly. The refusal existed for
		// a well-formed envelope and was missing for a malformed one.
		// (Cold review round three, 2026-09-12, R2 — reproduced.)
		return false, "the given envelope is malformed (" + reason + ")"
	}

	suffix := ""
	if bind == bindNotComparable {
		// Only this case discloses: the LOG's body is a shape this package
		// cannot read, which is legitimate for an entry kind it does not know.
		// The body is covered by the Merkle proof verified in step 1, so this
		// is a weaker binding rather than a forged one.
		suffix = "; envelope bound by payload hash only (" + reason + ")"
	}
	return true, "rekor inclusion proof and SET verified against the TUF trust root" + suffix
}

// bindResult says how strongly a Rekor entry was tied to a given envelope.
type bindResult int

const (
	bindOK                bindResult = iota // the entry demonstrably wraps this envelope
	bindMismatch                            // it demonstrably wraps a DIFFERENT one — refuse
	bindMalformedEnvelope                   // the ENVELOPE could not be read — refuse, it is attacker input
	bindNotComparable                       // the LOG BODY is a kind we cannot read — disclose
)

// bindEntryToEnvelope ties a Rekor entry body to the envelope presented for
// verification, strongest binding first.
//
//  1. envelopeHash. Rekor's dsse type stores sha256 of the envelope bytes AS
//     SUBMITTED, and Anchor submits the same bytes the record then stores
//     verbatim (brain/buildcert.go passes one `envelope` variable to both), so
//     this comparison is exact and safe. An earlier version of this code
//     skipped it on the theory that JSON re-serialization would break it —
//     that theory was wrong, and a review refuted it by reading Rekor's own
//     source. (Round three, R3.)
//  2. the signature set, for an entry that records signatures but no envelope
//     hash.
//
// The ENVELOPE failing to parse is never "not comparable": it is attacker
// input and must refuse. Only the LOG's body being an unreadable kind is
// disclosable. (Round three, R2.)
func bindEntryToEnvelope(body, dsseEnvelope []byte) (bindResult, string) {
	env, err := parseEnvelope(dsseEnvelope)
	if err != nil {
		return bindMalformedEnvelope, err.Error()
	}
	if len(env.sigs) == 0 {
		// Rekor never logs a zero-signature dsse envelope, so this cannot be
		// the envelope that was logged.
		return bindMalformedEnvelope, "it carries no signatures"
	}

	logged, ok := loggedBody(body)
	if !ok {
		return bindNotComparable, "the log entry body is not a shape this build can read"
	}
	if logged.envelopeHash != "" {
		sum := sha256.Sum256(dsseEnvelope)
		if !strings.EqualFold(logged.envelopeHash, hex.EncodeToString(sum[:])) {
			return bindMismatch, "envelope hash mismatch"
		}
		return bindOK, ""
	}
	if len(logged.signatures) == 0 {
		return bindNotComparable, "the log entry records neither an envelope hash nor any signature"
	}

	// SIGNATURE-SET EQUALITY, not containment. The previous check asked only
	// whether every signature the envelope CARRIES was logged, so an envelope
	// with signatures stripped down to a subset of the logged set passed, and
	// so did one with its payloadType rewritten — neither is an envelope Rekor
	// ever saw. Both directions are checked, and the payload type when the
	// log recorded one. (Round four, 2026-09-13, R1 — high; a defect in round
	// three's fix.)
	if logged.payloadType != "" && logged.payloadType != env.payloadType {
		return bindMismatch, "payload type mismatch"
	}
	inLog := decodedSet(logged.signatures)
	inEnv := decodedSet(env.sigs)
	for raw := range inEnv {
		if !inLog[raw] {
			return bindMismatch, "a signature it carries was never logged"
		}
	}
	for raw := range inLog {
		if !inEnv[raw] {
			return bindMismatch, "a logged signature is missing from it"
		}
	}
	return bindOK, ""
}

// decodedSet decodes each base64 signature and returns the set of raw bytes;
// a string that is not base64 in either alphabet is kept verbatim so that it
// can only ever match itself.
func decodedSet(sigs []string) map[string]bool {
	set := make(map[string]bool, len(sigs))
	for _, sig := range sigs {
		if raw, ok := decodeBase64Either(sig); ok {
			set[string(raw)] = true
		} else {
			set["\x00undecodable:"+sig] = true
		}
	}
	return set
}

// loggedEntryBody is the part of a Rekor entry body this package compares.
type loggedEntryBody struct {
	envelopeHash string
	payloadType  string
	signatures   []string
}

// loggedBody reads the signature material out of a Rekor entry body, for every
// kind whose layout is known.
//
// intoto v0.0.2 is handled as well as dsse v0.0.1, because sigstore-go returns
// a payload hash for BOTH — so an intoto entry passed step 3 and then skipped
// step 4 entirely, leaving the never-logged-signature hole open for any intoto
// entry anyone had logged over the same payload. The stated reason for
// skipping ("we cannot tell a bad case from a legitimate one") was simply
// false for that kind: intoto records its signatures, just at a different
// path. (Round three, R4.)
func loggedBody(body []byte) (loggedEntryBody, bool) {
	var raw struct {
		Kind string `json:"kind"`
		Spec struct {
			// dsse v0.0.1
			EnvelopeHash struct {
				Value string `json:"value"`
			} `json:"envelopeHash"`
			Signatures []struct {
				Signature string `json:"signature"`
			} `json:"signatures"`
			// intoto v0.0.2
			Content struct {
				Envelope struct {
					PayloadType string `json:"payloadType"`
					Signatures  []struct {
						Sig string `json:"sig"`
					} `json:"signatures"`
				} `json:"envelope"`
			} `json:"content"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return loggedEntryBody{}, false
	}
	out := loggedEntryBody{
		envelopeHash: raw.Spec.EnvelopeHash.Value,
		payloadType:  raw.Spec.Content.Envelope.PayloadType,
	}
	for _, s := range raw.Spec.Signatures {
		if s.Signature != "" {
			out.signatures = append(out.signatures, s.Signature)
		}
	}
	for _, s := range raw.Spec.Content.Envelope.Signatures {
		if s.Sig != "" {
			out.signatures = append(out.signatures, s.Sig)
		}
	}
	if out.envelopeHash == "" && len(out.signatures) == 0 {
		return loggedEntryBody{}, false
	}
	return out, true
}

// parsedEnvelope is the part of a DSSE envelope this package reads.
type parsedEnvelope struct {
	payload     []byte
	payloadType string
	sigs        []string
}

// parseEnvelope is THE envelope parser. Every question asked of an envelope —
// its payload hash, its signatures, its payload type — goes through it, so
// there is one definition of a readable envelope and no two doors can hold a
// different one.
//
// There used to be three parsers, one per field, each with its own idea of
// "valid": the hash door hashed a MISSING payload to sha256("") without
// complaint (round four, R3), and the signature door counted a signature
// object with no `sig` as a valid empty signature and so reported "tampered"
// for what was "unreadable" (round four, R4). The envelope is attacker input;
// whatever is not the shape DSSE defines is refused here, once.
func parseEnvelope(raw []byte) (parsedEnvelope, error) {
	var env struct {
		Payload     *string `json:"payload"`
		PayloadType string  `json:"payloadType"`
		Signatures  []struct {
			Sig *string `json:"sig"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return parsedEnvelope{}, errors.New("it is not a DSSE envelope this build can read")
	}
	if env.Payload == nil || *env.Payload == "" {
		return parsedEnvelope{}, errors.New("it has no payload")
	}
	payload, ok := decodeBase64Either(*env.Payload)
	if !ok {
		return parsedEnvelope{}, errors.New("its payload is not base64")
	}
	out := parsedEnvelope{payload: payload, payloadType: env.PayloadType}
	for i, s := range env.Signatures {
		if s.Sig == nil || *s.Sig == "" {
			return parsedEnvelope{}, fmt.Errorf("signature %d has no sig", i)
		}
		out.sigs = append(out.sigs, *s.Sig)
	}
	return out, nil
}

// envelopeSignatures reads a DSSE envelope's signatures through parseEnvelope.
// ok is false when the envelope is malformed; the caller REFUSES on that.
func envelopeSignatures(dsseEnvelope []byte) (sigs []string, ok bool) {
	env, err := parseEnvelope(dsseEnvelope)
	if err != nil {
		return nil, false
	}
	return env.sigs, true
}

// decodeBase64Either decodes standard or URL-safe base64, the same tolerance
// envelopePayloadSHA256 already applies to a DSSE payload.
func decodeBase64Either(s string) ([]byte, bool) {
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, true
	}
	if raw, err := base64.URLEncoding.DecodeString(s); err == nil {
		return raw, true
	}
	return nil, false
}

// parseInclusionProof unmarshals a stored inclusion proof AND requires it to
// be complete.
//
// A proof that UNMARSHALS is not a proof that is COMPLETE: `{}` parses into a
// zero models.InclusionProof whose RootHash, TreeSize, LogIndex, Hashes and
// Checkpoint are all nil, and tle.GenerateTransparencyLogEntry dereferences
// RootHash — so a malformed `rekor` field in a record used to PANIC inside
// VerifyInclusion instead of failing the check, taking `certify verify` down
// with it. No caller recovers: the only recover() in the tree is in
// internal/mission, and certverify calls VerifyInclusion directly.
// (Cold review 2026-09-12, R1 — reproduced by the reviewer's own script.)
//
// It calls the swagger model's OWN required-field validation rather than
// nil-checking the fields that happen to panic today. The rule is "the proof
// is complete"; enumerating today's three dereferences is the
// gate-that-lists-instead-of-deriving mistake this repository has already made
// six times.
func parseInclusionProof(raw []byte) (*models.InclusionProof, error) {
	var proof models.InclusionProof
	if err := json.Unmarshal(raw, &proof); err != nil {
		return nil, errors.New("inclusion proof is not well-formed")
	}
	if err := proof.Validate(strfmt.Default); err != nil {
		return nil, fmt.Errorf("inclusion proof is incomplete: %v", err)
	}
	return &proof, nil
}

// envelopePayloadSHA256 returns the SHA-256 of a DSSE envelope's decoded
// payload, matching how Rekor's dsse type stores the payload hash. It reads
// the envelope through parseEnvelope, so a missing or empty payload is an
// error rather than the hash of nothing.
func envelopePayloadSHA256(dsseEnvelope []byte) ([32]byte, error) {
	env, err := parseEnvelope(dsseEnvelope)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(env.payload), nil
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
