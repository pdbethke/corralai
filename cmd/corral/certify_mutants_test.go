// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
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
		Format: adequacy.MutantSetFormat, Files: map[string]adequacy.MutantSetEntry{},
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
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {
				// Derived from OTHER bytes than what is on disk.
				ParentSHA256: shaOf("package pkg\n\nfunc A() int { return 2 }\n"),
				Mutants:      []adequacy.RecordedMutant{{ID: "m1", Replace: "package pkg\n\nfunc A() int { return 0 }\n"}},
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
		Format: adequacy.MutantSetFormat, Files: map[string]adequacy.MutantSetEntry{},
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
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {ParentSHA256: shaOf("x"), Mutants: []adequacy.RecordedMutant{{ID: "m1", Replace: "y"}}},
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
	if probe.Format != "corral-mutants-2" {
		t.Fatalf("format = %q, want corral-mutants-2", probe.Format)
	}
}

// TestRecorderWritesV2Only: --record-mutants writes HUNKS. A recorder that
// still emitted v1 would keep shipping a whole mutated file per mutant — the
// cost the representation change exists to remove — and would do it silently,
// since a v1 document still reads.
func TestRecorderWritesV2Only(t *testing.T) {
	const src = "def add(a, b):\n    return a + b\n"
	rec := newMutantSetRecorder()
	rec.sink("x.py", []adequacy.Mutant{{
		ID: "m1", ParentSHA256: shaOf(src),
		Search: "return a + b", Replace: "return a - b",
	}})
	p := filepath.Join(t.TempDir(), "s.json")
	if _, err := rec.write(p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"format": "corral-mutants-2"`) {
		t.Fatalf("the recorder must write corral-mutants-2, got:\n%s", raw)
	}
	if strings.Contains(string(raw), `"code"`) {
		t.Fatalf("a recorded mutant must carry its hunk, never a copy of the file:\n%s", raw)
	}
	set, err := adequacy.ReadMutantSet(p)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.MutantsFor("x.py", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Search != "return a + b" || ms[0].Replace != "return a - b" {
		t.Fatalf("the hunk did not survive the round trip: %+v", ms)
	}
	code, aerr := ms[0].Apply(src)
	if aerr != nil || code != "def add(a, b):\n    return a - b\n" {
		t.Fatalf("replayed hunk applies to %q (err=%v)", code, aerr)
	}
}

// TestAuditConfigKeySeparatesAReplayFromAGeneratedRun: a replayed run and a
// generated one are not the same measurement of the same content, so they
// must not share a verdict-cache key. Without this the cache would hand a
// `--mutants` scan a verdict earned against a completely different, model-
// authored exam — the same model-blind key bug that once let one model's
// verdict be served for another's, in the one dimension --mutants controls.
func TestAuditConfigKeySeparatesAReplayFromAGeneratedRun(t *testing.T) {
	generated := auditConfigKey(false, "coverage-context", nil, "", "")
	replayA := auditConfigKey(false, "coverage-context", nil, shaOf("set-a"), "")
	replayB := auditConfigKey(false, "coverage-context", nil, shaOf("set-b"), "")

	if generated == replayA {
		t.Error("a replayed run shares a cache key with a generated one — a cached verdict from another exam would be served as this one's")
	}
	if replayA == replayB {
		t.Error("two DIFFERENT recorded sets share a cache key — the key does not identify the exam")
	}
	if replayA != auditConfigKey(false, "coverage-context", nil, shaOf("set-a"), "") {
		t.Error("the key is not stable for the same set")
	}
}

// TestLocalMutantSetKeyRoundTripsWhatItRecords is fix-round-1 finding 1: the
// recorder writes each entry under advpool.RunSpec.CodePath — buildJailWiring's
// codeKey — while the --mutants lookup keyed on the RAW --code string. Any
// --code with a directory component therefore recorded as `x.py` and was then
// refused when the byte-identical command tried to replay it: a set that
// cannot replay the run that produced it, with the refusal looking exactly
// like the tampering the check exists to catch.
func TestLocalMutantSetKeyRoundTripsWhatItRecords(t *testing.T) {
	root := t.TempDir()
	const src = "def add(a, b):\n    return a + b\n"
	mustWrite(t, filepath.Join(root, "sub", "dir", "x.py"), src)

	t.Run("single-file mode records and looks up the same basename", func(t *testing.T) {
		// The recorder's key is what the driver hands MutantSink, which in
		// single-file mode is filepath.Base(--code).
		rec := newMutantSetRecorder()
		rec.sink("x.py", []adequacy.Mutant{{ID: "m1", Replace: "bad", ParentSHA256: shaOf(src)}})
		setPath := filepath.Join(t.TempDir(), "s.json")
		if _, err := rec.write(setPath); err != nil {
			t.Fatal(err)
		}
		set, err := adequacy.ReadMutantSet(setPath)
		if err != nil {
			t.Fatal(err)
		}

		// Replay with the SAME --code the recording run used.
		codeArg := filepath.Join(root, "sub", "dir", "x.py")
		key, fsPath := localMutantSetKey("", codeArg)
		if fsPath != codeArg {
			t.Fatalf("fsPath = %q, want the --code path %q", fsPath, codeArg)
		}
		if _, err := set.MutantsFor(key, src); err != nil {
			t.Fatalf("a run cannot replay its own recording: key %q: %v", key, err)
		}
	})

	t.Run("repo-dir mode records and looks up the repo-relative path", func(t *testing.T) {
		rec := newMutantSetRecorder()
		rec.sink("sub/dir/x.py", []adequacy.Mutant{{ID: "m1", Replace: "bad", ParentSHA256: shaOf(src)}})
		setPath := filepath.Join(t.TempDir(), "s.json")
		if _, err := rec.write(setPath); err != nil {
			t.Fatal(err)
		}
		set, err := adequacy.ReadMutantSet(setPath)
		if err != nil {
			t.Fatal(err)
		}

		key, fsPath := localMutantSetKey(root, "sub/dir/x.py")
		if key != "sub/dir/x.py" {
			t.Fatalf("key = %q, want the repo-relative path a --repo scan also records", key)
		}
		if fsPath != filepath.Join(root, "sub", "dir", "x.py") {
			t.Fatalf("fsPath = %q, want the file inside --repo-dir", fsPath)
		}
		if _, err := set.MutantsFor(key, src); err != nil {
			t.Fatalf("a --repo-dir run cannot replay its own recording: key %q: %v", key, err)
		}
	})
}

// TestCertifyLocalReplaysItsOwnRecordingKey drives the two flags through
// runCertifyLocal itself, in SINGLE-FILE mode with a --code that carries a
// directory component — the exact shape that broke. The recording run writes
// its entry under the driver's codeKey (`x.py`); replaying the identical
// command used to look the file up under the raw `<dir>/x.py` and refuse it.
// The audit itself cannot run here (no provider key), which is fine: the
// --mutants check runs before any of that, so the assertion is on the refusal
// that must NOT happen.
func TestCertifyLocalReplaysItsOwnRecordingKey(t *testing.T) {
	root := t.TempDir()
	const src = "def add(a, b):\n    return a + b\n"
	codeArg := filepath.Join(root, "examples", "x.py")
	mustWrite(t, codeArg, src)

	// What the recording run wrote: keyed by advpool.RunSpec.CodePath, which
	// in single-file mode is filepath.Base(--code).
	setPath := filepath.Join(t.TempDir(), "s.json")
	rec := newMutantSetRecorder()
	rec.sink("x.py", []adequacy.Mutant{{ID: "m1", Replace: "def add(a, b):\n    return a - b\n", ParentSHA256: shaOf(src)}})
	if _, err := rec.write(setPath); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	runCertifyLocal([]string{
		"--code", codeArg, "--goal", "add must add",
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--mutants", setPath,
	}, &out, &errb)

	if strings.Contains(errb.String(), "--mutants:") {
		t.Fatalf("the run refused its own recorded set:\n%s", errb.String())
	}
	if !strings.Contains(out.String(), "replaying 1 recorded mutant(s)") {
		t.Fatalf("expected the replay disclosure on stdout, got:\nstdout=%s\nstderr=%s", out.String(), errb.String())
	}
}

// TestMutantSetRecorderReportDisclosesCacheHits is fix-round-1 finding 2: a
// file served from the verdict cache runs no dev pass, never reaches the
// sink, and is silently absent from the recorded set — which then refuses its
// own replay for that file with nothing anywhere explaining why. The record
// line has to carry the denominator and the reason, not just the count.
func TestMutantSetRecorderReportDisclosesCacheHits(t *testing.T) {
	rec := newMutantSetRecorder()
	rec.sink("pkg/a.go", []adequacy.Mutant{{ID: "m1", Replace: "x", ParentSHA256: shaOf("a")}})
	// No parent hash: recorded as skipped, never as a replayable entry.
	rec.sink("pkg/b.go", []adequacy.Mutant{{ID: "m1", Replace: "x"}})

	var buf bytes.Buffer
	// 4 audited: a (recorded), b (skipped), and 2 served from the cache.
	rec.report(&buf, "set.json", 1, 4, 2)
	got := buf.String()

	if !strings.Contains(got, "wrote 1 of 4 audited file(s)") {
		t.Errorf("the record line must carry the denominator, got:\n%s", got)
	}
	if !strings.Contains(got, "2 file(s) served from the verdict cache and cannot be replayed") {
		t.Errorf("cache-served files must be disclosed, got:\n%s", got)
	}
	if !strings.Contains(got, "re-run without the cache to record them") {
		t.Errorf("the disclosure must say what to do about it, got:\n%s", got)
	}
	if !strings.Contains(got, "pkg/b.go") {
		t.Errorf("a file skipped for a missing parent hash must still be named, got:\n%s", got)
	}

	// The `certify --local` shape: no denominator, no cache, no extra lines.
	var plain bytes.Buffer
	newMutantSetRecorder().report(&plain, "set.json", 0, 0, 0)
	if strings.Contains(plain.String(), "audited file(s)") || strings.Contains(plain.String(), "verdict cache") {
		t.Errorf("a cache-less single-file run must not claim a denominator it has not got, got:\n%s", plain.String())
	}
}

// TestCertifyRepoRefusesAStaleMutantSetBeforeRunningTheInstrumentedSuite is
// F3: the scan's own selection-evidence run (the SAME instrumented suite
// pass candidacy widening now uses) must never execute before --mutants,
// the jail preflight and the provider preflight have all had their say — a
// stale/missing --mutants file (or a missing key) must be refused BEFORE a
// real, possibly-minutes-long suite run, not after.
func TestCertifyRepoRefusesAStaleMutantSetBeforeRunningTheInstrumentedSuite(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	setPath := filepath.Join(root, "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {
				ParentSHA256: shaOf("package pkg\n\nfunc A() int { return 2 }\n"), // stale
				Mutants:      []adequacy.RecordedMutant{{ID: "m1", Replace: "package pkg\n\nfunc A() int { return 0 }\n"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	orig := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = orig })
	ran := false
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string) reposcan.SelectionEvidence {
		ran = true
		return reposcan.SelectionEvidence{Ran: true}
	}

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--substrate", substrateWorkspace, "--mutants", setPath,
		"--", "false",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if ran {
		t.Error("the instrumented selection suite ran before the stale --mutants refusal — the exact cost F3 exists to avoid")
	}
}

// TestCertifyRepoDiffBaseWithNothingSelectedNeverRunsTheInstrumentedSuite is
// F3's second half: restoring a cost guard for an empty --diff-base scan.
// Scoped to --diff-base specifically (not to "pairing found zero
// candidates" in general) — see the guard's own comment in runCertifyRepo
// for why a blanket `len(selected) > 0` would regress the design's own
// itsdangerous-shaped headline case.
func TestCertifyRepoDiffBaseWithNothingSelectedNeverRunsTheInstrumentedSuite(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)
	// A second commit that touches NOTHING under pkg/ — the diff against
	// base selects zero candidates and rescues no unpairable file either.
	mustWrite(t, filepath.Join(root, "README.md"), "docs only\n")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "docs", "--no-gpg-sign")

	orig := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = orig })
	ran := false
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string) reposcan.SelectionEvidence {
		ran = true
		return reposcan.SelectionEvidence{Ran: true}
	}

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--substrate", substrateWorkspace, "--diff-base", base,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (nothing in scope is not a failure): stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if ran {
		t.Error("the instrumented selection suite ran for a --diff-base scan that selected nothing and had nothing to rescue")
	}
}
