// SPDX-License-Identifier: Elastic-2.0

// Package certify builds a hash-linked, tamper-evident ledger of build steps,
// wraps the ledger head in an in-toto/SLSA provenance attestation, and
// signs/verifies the head with Ed25519.
//
// It is a pure, dependency-light core: it has no storage, no MCP, no CLI
// dependencies. Callers (a DuckDB store, a brain MCP tool, the `corral
// certify` CLI) build on top of these primitives.
package certify

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// genesisPrev is the "prev" hash of the first step in a ledger: 64 zero
// characters, the same width as a hex-encoded sha256 digest.
const genesisPrev = "0000000000000000000000000000000000000000000000000000000000000000"

// slsaProvenanceV1 is the in-toto predicateType for SLSA Provenance v1.
const slsaProvenanceV1 = "https://slsa.dev/provenance/v1"

// Step is one entry in a build ledger: a single recorded event (context,
// execution, review, etc.) in a corral certify run.
type Step struct {
	Seq     int            `json:"seq"`
	TS      float64        `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor"`
	Model   string         `json:"model"`
	Subject string         `json:"subject"`
	Detail  map[string]any `json:"detail"`
	Prev    string         `json:"prev"`
	Hash    string         `json:"-"`
}

// BuildRecord describes the overall build/run that a BuildAttestation
// certifies.
type BuildRecord struct {
	Repo         string
	Commit       string
	Branch       string
	Actor        string
	Command      string
	ExitCode     int
	DurationS    float64
	OutputDigest string
	ProducedBy   []string
	StartedTS    float64
	FinishedTS   float64
}

// stepHash returns the deterministic sha256 hash (hex) of a step, computed
// over the JSON of its fields excluding Hash (Hash carries json:"-", so
// json.Marshal never includes the in-progress hash). Detail is a
// map[string]any; encoding/json marshals map keys in sorted order, so this
// is reproducible across processes without any custom sorting.
func stepHash(s Step) string {
	b, err := json.Marshal(s)
	if err != nil {
		// Step's fields (ints, strings, float64, map[string]any) are always
		// JSON-marshalable; a failure here indicates a programming error.
		panic("certify: step is not JSON-marshalable: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BuildLedger assigns Seq/Prev/Hash to each step in order, chaining each
// step's hash into the next step's Prev. It returns the ledger with those
// fields populated and the head (the last step's hash; genesis "0"*64 if
// steps is empty).
func BuildLedger(steps []Step) (out []Step, head string) {
	out = make([]Step, len(steps))
	prev := genesisPrev
	for i, s := range steps {
		s.Seq = i
		s.Prev = prev
		s.Hash = stepHash(s)
		out[i] = s
		prev = s.Hash
	}
	head = prev
	return out, head
}

// VerifyLedger recomputes the hash chain over steps and confirms it lands on
// head. It returns false with a diagnostic message at the first broken link,
// altered step, or head mismatch.
func VerifyLedger(steps []Step, head string) (bool, string) {
	prev := genesisPrev
	for _, s := range steps {
		if s.Prev != prev {
			return false, "broken link at seq " + strconv.Itoa(s.Seq) + " (prev mismatch)"
		}
		if stepHash(s) != s.Hash {
			return false, "altered step at seq " + strconv.Itoa(s.Seq) + " (hash mismatch)"
		}
		prev = s.Hash
	}
	if prev != head {
		return false, "head does not match the ledger's last hash"
	}
	return true, "OK"
}

// BuildAttestation wraps a BuildRecord and a ledger head in an in-toto
// Statement v1 carrying an SLSA Provenance v1 predicate. The statement's
// subject digest is the ledger head, binding the attestation to the exact
// signed, ordered record of every build step. Models that produced the
// change are named in resolvedDependencies (materials); the command/exit
// code/pass-fail certification is carried in byproducts.
func BuildAttestation(r BuildRecord, head string) map[string]any {
	models := r.ProducedBy
	if models == nil {
		models = []string{}
	}
	resolvedDeps := make([]map[string]any, 0, len(models))
	for _, m := range models {
		resolvedDeps = append(resolvedDeps, map[string]any{"uri": "model:" + m})
	}

	return map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{
				"name":   r.Repo + "@" + r.Commit,
				"digest": map[string]string{"sha256": head},
			},
		},
		"predicateType": slsaProvenanceV1,
		"predicate": map[string]any{
			"buildDefinition": map[string]any{
				"buildType": "https://corralai.dev/certify/v1",
				"externalParameters": map[string]any{
					"repo":    r.Repo,
					"commit":  r.Commit,
					"branch":  r.Branch,
					"command": r.Command,
				},
				"internalParameters": map[string]any{
					"actor": r.Actor,
				},
				"resolvedDependencies": resolvedDeps,
			},
			"runDetails": map[string]any{
				"builder": map[string]any{
					"id":                  "https://corralai.dev/certify",
					"builderDependencies": resolvedDeps,
				},
				"metadata": map[string]any{
					"startedOn":  r.StartedTS,
					"finishedOn": r.FinishedTS,
				},
				"byproducts": []map[string]any{
					{
						"name":      "accountability/tamper-evident-ledger",
						"mediaType": "application/vnd.corralai.build-ledger+json",
						"digest":    map[string]string{"sha256": head},
					},
					{
						"name":      "certification/execution",
						"mediaType": "application/vnd.corralai.certification+json",
						"annotations": map[string]any{
							"command":      r.Command,
							"exitCode":     r.ExitCode,
							"passed":       r.ExitCode == 0,
							"durationS":    r.DurationS,
							"outputDigest": r.OutputDigest,
						},
					},
				},
			},
		},
	}
}

// Sign returns the hex-encoded Ed25519 signature of head under priv.
func Sign(head string, priv ed25519.PrivateKey) string {
	sig := ed25519.Sign(priv, []byte(head))
	return hex.EncodeToString(sig)
}

// VerifySig reports whether sigHex is a valid Ed25519 signature of head
// under pub. It returns false (not an error) on a malformed sigHex.
func VerifySig(head, sigHex string, pub ed25519.PublicKey) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(head), sig)
}

// CanonicalStatement returns deterministic JSON bytes for stmt.
//
// json.Marshal is deterministic here because every value inside a statement
// built by BuildAttestation is JSON-native: map[string]any and
// map[string]string encode with keys sorted lexicographically by
// encoding/json, []string and []map[string]any preserve their original
// order, and there are no Go structs whose field order could vary from
// their declaration. That means two calls to CanonicalStatement on
// equivalent maps always produce byte-identical output, which is what lets
// a detached Ed25519 signature over these bytes be verified independently
// later. Do not introduce a Go struct or a third-party canonicalizer here;
// the plain map shape is what makes this guarantee hold.
func CanonicalStatement(stmt map[string]any) ([]byte, error) {
	return json.Marshal(stmt)
}

// SignStatement signs the canonical JSON encoding of stmt with priv and
// returns the hex-encoded signature along with the exact canonical bytes
// that were signed. Callers must persist canonical as-is (not a re-marshal)
// so that VerifyStatement can later verify over the identical bytes. It
// returns an error (never panics) if stmt cannot be marshaled.
func SignStatement(stmt map[string]any, priv ed25519.PrivateKey) (sigHex string, canonical []byte, err error) {
	canonical, err = CanonicalStatement(stmt)
	if err != nil {
		return "", nil, err
	}
	sigHex = hex.EncodeToString(ed25519.Sign(priv, canonical))
	return sigHex, canonical, nil
}

// VerifyStatement reports whether sigHex is a valid Ed25519 signature of the
// canonical bytes under pub. It verifies over the stored canonical bytes
// directly (not a re-marshal of any statement), which sidesteps any
// float/int re-serialization ambiguity. It returns false (not an error) on
// a malformed sigHex.
func VerifyStatement(canonical []byte, sigHex string, pub ed25519.PublicKey) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, canonical, sig)
}
