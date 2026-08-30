// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/reposcan"
)

func shaOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// TestMutantSetFlagsParseOnBothCommands: the two flags must exist and PARSE
// on the real commands, not merely be documented — the same rule
// TestActionPassesTopOnlyWhenSet applies to --top.
func TestMutantSetFlagsParseOnBothCommands(t *testing.T) {
	dir := t.TempDir()
	setPath := filepath.Join(dir, "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: "corral-mutants-1", Files: map[string]adequacy.MutantSetEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	recPath := filepath.Join(dir, "recorded.json")

	t.Run("repo", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{
			"--repo", t.TempDir(), "--dry-run",
			"--mutants", setPath, "--record-mutants", recPath,
		}, &out, &errb)
		if code != 0 {
			t.Fatalf("certify --repo rejected --mutants/--record-mutants: exit %d, stderr=%s", code, errb.String())
		}
	})

	t.Run("local", func(t *testing.T) {
		var out, errb bytes.Buffer
		// No --code, so the command stops at its own first usage check. What
		// this proves is that flag PARSING got that far: an unregistered flag
		// fails earlier, with flag's own "not defined" message.
		runCertifyLocal([]string{"--mutants", setPath, "--record-mutants", recPath}, &out, &errb)
		if strings.Contains(errb.String(), "not defined") {
			t.Fatalf("certify --local does not define --mutants/--record-mutants: %s", errb.String())
		}
	})
}

// TestCertifyRepoRefusesAStaleMutantSet: a recorded set is tied to the exact
// bytes it was derived from. When a selected file has changed, replaying is
// not "close enough" — the mutants are edits of source that no longer exists.
// The scan must refuse (exit 2), name the file, and do so BEFORE any model is
// resolved: the whole point of --mutants is that it costs no generation, so
// paying for a goal derivation on the way to a refusal would be absurd.
func TestCertifyRepoRefusesAStaleMutantSet(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	const src = "package pkg\n\nfunc A() int { return 1 }\n"
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), src)
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	setPath := filepath.Join(root, "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: "corral-mutants-1",
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {
				// Derived from OTHER bytes than what is on disk.
				ParentSHA256: shaOf("package pkg\n\nfunc A() int { return 2 }\n"),
				Mutants:      []adequacy.RecordedMutant{{ID: "m1", Code: "package pkg\n\nfunc A() int { return 0 }\n"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// The deriver must never be built: the refusal comes first.
	prev := certifyRepoDeriver
	t.Cleanup(func() { certifyRepoDeriver = prev })
	derived := false
	certifyRepoDeriver = func(model string) (reposcan.Deriver, error) {
		derived = true
		return prev(model)
	}

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--derive-model", testHerdWriter,
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--substrate", substrateWorkspace,
		"--mutants", setPath,
		"--", "false",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2 for a mutant set derived from other bytes: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "pkg/a.go") {
		t.Errorf("the refusal must name the file whose bytes moved, got: %s", errb.String())
	}
	if derived {
		t.Error("a goal deriver was constructed on the way to refusing a stale mutant set — the refusal must precede every model call")
	}
}

// TestCertifyRepoRefusesAMutantSetMissingASelectedFile: replaying a set that
// simply has no entry for an audited file must be refused too, not silently
// fall back to generating one — that would produce a run half-replayed and
// half-fresh, which is exactly the incomparability --mutants exists to remove.
func TestCertifyRepoRefusesAMutantSetMissingASelectedFile(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	setPath := filepath.Join(root, "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: "corral-mutants-1", Files: map[string]adequacy.MutantSetEntry{},
	}); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--derive-model", testHerdWriter,
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--substrate", substrateWorkspace,
		"--mutants", setPath,
		"--", "false",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2 for a set with no entry for a selected file: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "pkg/a.go") {
		t.Errorf("the refusal must name the unrecorded file, got: %s", errb.String())
	}
}

// TestMutantSetFileRoundTripsThroughTheWriter pins the on-disk document the
// two flags exchange — a set written by one run has to be readable by the
// next, so the format string and the shape are part of the contract.
func TestMutantSetFileRoundTripsThroughTheWriter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "set.json")
	if err := adequacy.WriteMutantSet(p, adequacy.MutantSetFile{
		Format: "corral-mutants-1",
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {ParentSHA256: shaOf("x"), Mutants: []adequacy.RecordedMutant{{ID: "m1", Code: "y"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Format != "corral-mutants-1" {
		t.Fatalf("format = %q, want corral-mutants-1", probe.Format)
	}
}

// TestAuditConfigKeySeparatesAReplayFromAGeneratedRun: a replayed run and a
// generated one are not the same measurement of the same content, so they
// must not share a verdict-cache key. Without this the cache would hand a
// `--mutants` scan a verdict earned against a completely different, model-
// authored exam — the same model-blind key bug that once let one model's
// verdict be served for another's, in the one dimension --mutants controls.
func TestAuditConfigKeySeparatesAReplayFromAGeneratedRun(t *testing.T) {
	generated := auditConfigKey(false, "coverage-context", nil, "")
	replayA := auditConfigKey(false, "coverage-context", nil, shaOf("set-a"))
	replayB := auditConfigKey(false, "coverage-context", nil, shaOf("set-b"))

	if generated == replayA {
		t.Error("a replayed run shares a cache key with a generated one — a cached verdict from another exam would be served as this one's")
	}
	if replayA == replayB {
		t.Error("two DIFFERENT recorded sets share a cache key — the key does not identify the exam")
	}
	if replayA != auditConfigKey(false, "coverage-context", nil, shaOf("set-a")) {
		t.Error("the key is not stable for the same set")
	}
}
