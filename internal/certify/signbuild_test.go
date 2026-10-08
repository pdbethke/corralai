// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"testing"
)

// oldRecipe is the body signBuildLocally and brain.certifyBuild each carried
// before SignBuild existed (they were line-for-line the same apart from the
// actor, the key id and where the duration came from). It is kept for one
// commit as the proof that SignBuild reproduces it byte for byte.
func oldRecipe(t *testing.T, rec BuildRecord, rawDurationS float64, priv ed25519.PrivateKey, keyID string) SignedBuild {
	t.Helper()
	steps := []Step{
		{
			Kind: "context", Actor: rec.Actor, Subject: rec.Repo + "@" + rec.Commit,
			Detail: map[string]any{"repo": rec.Repo, "commit": rec.Commit, "branch": rec.Branch},
		},
		{
			Kind: "execution", Actor: rec.Actor, Subject: rec.Command,
			Detail: map[string]any{
				"exit_code": rec.ExitCode, "ok": rec.ExitCode == 0,
				"duration_s": rawDurationS, "output_digest": rec.OutputDigest,
			},
		},
	}
	built, head := BuildLedger(steps)
	stmt := BuildAttestation(rec, head)
	envelope, err := SignDSSE(stmt, priv, keyID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalStatement(stmt)
	if err != nil {
		t.Fatal(err)
	}
	stepsJSON, err := MarshalSteps(built)
	if err != nil {
		t.Fatal(err)
	}
	var stepsOut []map[string]any
	if err := json.Unmarshal(stepsJSON, &stepsOut); err != nil {
		t.Fatal(err)
	}
	return SignedBuild{Steps: stepsOut, Head: head, Statement: stmt, Envelope: envelope, Canonical: canonical}
}

func TestSignBuildIsTheRecipeBothCallersUsed(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	cases := []struct {
		name  string
		rec   BuildRecord
		dur   float64
		keyID string
	}{
		{"cli", BuildRecord{Repo: "r", Commit: "c", Branch: "b", Actor: "corral-certify",
			Command: "go test ./...", ExitCode: 1, DurationS: SecondsOrUnmeasured(2.5),
			OutputDigest: "sha256:ab", ProducedBy: []string{"corral"}}, 2.5, "corral-certify"},
		{"brain", BuildRecord{Repo: "o/n", Commit: "deadbeef", Branch: "", Actor: "ci-principal",
			Command: "make test", ExitCode: 0, DurationS: SecondsOrUnmeasured(0),
			OutputDigest: "", ProducedBy: []string{"m1", "m2"}}, 0, "brain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SignBuild(tc.rec, tc.dur, priv, tc.keyID)
			if err != nil {
				t.Fatal(err)
			}
			want := oldRecipe(t, tc.rec, tc.dur, priv, tc.keyID)
			if got.Head != want.Head || !bytes.Equal(got.Envelope, want.Envelope) || !bytes.Equal(got.Canonical, want.Canonical) {
				t.Fatalf("SignBuild drifted from the recipe it replaces:\n got head %s\nwant head %s", got.Head, want.Head)
			}
			gs, _ := json.Marshal(got.Steps)
			ws, _ := json.Marshal(want.Steps)
			if !bytes.Equal(gs, ws) {
				t.Fatalf("steps differ:\n%s\n%s", gs, ws)
			}
		})
	}
}
