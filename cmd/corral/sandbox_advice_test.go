// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// swiftRefusal is Swift's own JailPreflight refusal, the one wording every
// door must print for it.
func swiftRefusal(t *testing.T) string {
	t.Helper()
	p, ok := lang.ByName("swift")
	if !ok {
		t.Fatal("no swift plugin")
	}
	err := lang.JailPreflight(p, nil)
	if err == nil {
		t.Fatal("swift's JailPreflight refused nothing")
	}
	return err.Error()
}

// writeSwiftPackage is the smallest SwiftPM package the scan pairs: one
// source and its test.
func writeSwiftPackage(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "Package.swift"), "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"A\")\n")
	mustWrite(t, filepath.Join(root, "Sources", "A", "B.swift"), "public func b() -> Int { 1 }\n")
	mustWrite(t, filepath.Join(root, "Tests", "ATests", "BTests.swift"), "import XCTest\n@testable import A\nfinal class BTests: XCTestCase { func testB() { XCTAssertEqual(b(), 1) } }\n")
}

// failSandbox makes every sandbox resolution fail as a host without bwrap
// does, through the real advice formatter.
func failSandbox(t *testing.T) {
	t.Helper()
	orig := resolveJailFn
	resolveJailFn = func(_ string, fallback bool) (sandbox.Isolator, error) {
		return nil, bwrapUnavailableError(errors.New(`bwrap backend unavailable: bwrap not found on PATH`), fallback)
	}
	t.Cleanup(func() { resolveJailFn = orig })
}

// S1: on a host whose sandbox will not start, a Swift repository's dry run
// with --evidence used to advise installing bwrap or --jail container, and
// both lead only to Swift's own refusal. The door now prints that refusal,
// which names `certify --repo --substrate workspace`, in place of the
// sandbox advice.
func TestCertifyRepoEvidenceSandboxRefusalForALanguageTheSandboxNeverRuns(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	failSandbox(t)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--no-selection-cache", "--", "swift", "test"}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got := errb.String()
	for _, want := range []string{"bwrap not found on PATH", swiftRefusal(t), "--substrate workspace"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"--jail container", "apparmor_parser"} {
		if strings.Contains(got, never) {
			t.Errorf("stderr still gives sandbox advice that cannot run Swift (%q):\n%s", never, got)
		}
	}
}

// The same door for a language the sandbox does run keeps its advice
// byte for byte: nothing changes unless the language's own JailPreflight
// refuses outright.
func TestCertifyRepoSandboxRefusalKeepsItsAdviceForALanguageTheSandboxRuns(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	failSandbox(t)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, errb.String())
	}
	want := "corral certify --repo: " + bwrapUnavailableError(errors.New(`bwrap backend unavailable: bwrap not found on PATH`), true).Error() + "\n"
	if errb.String() != want {
		t.Errorf("stderr changed for a Go repository:\n got %q\nwant %q", errb.String(), want)
	}
}

// The reviewer's probe: a Swift package plus one script with no test. The
// script is no file this run can grade (`swift test` never runs it, and it
// is no candidate), so it must not bring the sandbox advice back.
func TestCertifyRepoSandboxRefusalIgnoresAnUnpairedFileOfAnotherLanguage(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	mustWrite(t, filepath.Join(root, "scripts", "release.py"), "print('release')\n")
	failSandbox(t)

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--no-selection-cache", "--", "swift", "test"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, errb.String())
	}
	got := errb.String()
	if !strings.Contains(got, swiftRefusal(t)) {
		t.Errorf("stderr lacks Swift's refusal:\n%s", got)
	}
	for _, never := range []string{"--jail container", "apparmor_parser", "would still not run"} {
		if strings.Contains(got, never) {
			t.Errorf("an unpaired .py brought back sandbox advice (%q):\n%s", never, got)
		}
	}
}

// A run that would audit files in a language the sandbox runs AND in one it
// never runs states both plainly: the advice, saying which files it is for,
// and the refusal, saying which files it is for.
func TestCertifyRepoSandboxRefusalForAMixedRepositoryStatesBoth(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	failSandbox(t)

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, errb.String())
	}
	got := errb.String()
	want := "--jail container" // the advice, kept for the Go files
	note := "That advice is for the Go files this run would audit. corral's sandbox never runs its Swift files, on any host:\n" + swiftRefusal(t)
	if !strings.Contains(got, want) || !strings.Contains(got, note) {
		t.Errorf("stderr does not state both plainly; want %q and %q in:\n%s", want, note, got)
	}
	if strings.Index(got, want) > strings.Index(got, note) {
		t.Errorf("the note must follow the advice it qualifies:\n%s", got)
	}
}

// `corral tests map` asks the same preflight, so it gives the same answer.
func TestTestsMapSandboxRefusalForALanguageTheSandboxNeverRuns(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	failSandbox(t)

	var out, errb bytes.Buffer
	code := runTests([]string{"map", "--repo-dir", root, "--workers", "1", "--no-selection-cache", "--out", filepath.Join(t.TempDir(), "m.json"), "--", "swift", "test"}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got := errb.String()
	if !strings.Contains(got, swiftRefusal(t)) || strings.Contains(got, "--jail container") {
		t.Errorf("tests map did not swap the sandbox advice for Swift's refusal:\n%s", got)
	}
}

// doctor's sandbox check, for a --code file in a language the sandbox never
// runs, prints that language's refusal instead of "install bubblewrap", and
// says doctor cannot check the workspace substrate the refusal names.
func TestDoctorSandboxCheckForALanguageTheSandboxNeverRuns(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	var out, errb bytes.Buffer
	code := runDoctor([]string{"--repo", root, "--jail", "no-such-backend", "--code", filepath.Join(root, "Sources", "A", "B.swift")}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got := out.String()
	for _, want := range []string{"[FAIL] sandbox starts", "no-such-backend", swiftRefusal(t), flattenDetail(doctorWorkspaceNote)} {
		if !strings.Contains(got, flattenDetail(want)) {
			t.Errorf("doctor output lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"install bubblewrap", "--jail container"} {
		if strings.Contains(got, never) {
			t.Errorf("doctor still advises %q for Swift:\n%s", never, got)
		}
	}
}

// For any other --code language, and with no --code at all, doctor keeps its
// sandbox advice, and says it cannot check a workspace-substrate run.
func TestDoctorSandboxCheckKeepsItsAdviceAndSaysWhatItCannotCheck(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	for name, args := range map[string][]string{
		"go --code": {"--repo", root, "--jail", "no-such-backend", "--code", filepath.Join(root, "a.go")},
		"no --code": {"--repo", root, "--jail", "no-such-backend"},
	} {
		var out, errb bytes.Buffer
		if code := runDoctor(args, &out, &errb); code != 1 {
			t.Fatalf("%s: exit %d, want 1; stderr=%s", name, code, errb.String())
		}
		got := out.String()
		for _, want := range []string{"install bubblewrap", doctorWorkspaceNote} {
			if !strings.Contains(got, flattenDetail(want)) {
				t.Errorf("%s: doctor output lacks %q:\n%s", name, want, got)
			}
		}
	}
}

// flattenDetail is s as printCheck indents a multi-line detail: only its
// first line, which is enough to find it.
func flattenDetail(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return first
}

// With a sandbox that starts, doctor's toolchain check for a language the
// sandbox never runs prints the refusal (as before) and what doctor cannot
// check, since that check can never pass. The refusal is decided before
// anything runs in the sandbox, so no sandbox is needed here.
func TestDoctorToolchainCheckForALanguageTheSandboxNeverRunsSaysWhatItCannotCheck(t *testing.T) {
	p, _ := lang.ByName("swift")
	r := checkToolchain(nil, []string{"swift", "test"}, nil, p)
	if r.ok {
		t.Fatalf("the toolchain check passed for Swift: %+v", r)
	}
	if !strings.Contains(r.detail, swiftRefusal(t)) || !strings.Contains(r.detail, doctorWorkspaceNote) {
		t.Errorf("detail = %q, want Swift's refusal and doctorWorkspaceNote", r.detail)
	}
}

// The exact case a review found: doctor --code X.swift, a sandbox that
// starts, models named, and no test command. The toolchain check used to
// return "skipped" before asking anything, so doctor printed "All checks
// passed" for a file the default jailed certify then refuses. It now fails
// that check with Swift's refusal (doctor's state for a check that did not
// pass; it has no "could not check" state that blocks the all-passed line),
// says what it could not check and exits 1. Asking the declaration runs no
// sandbox, so this needs none.
func TestDoctorDoesNotPassASwiftFileWithoutATestCommand(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	orig := doctorResolveSandbox
	doctorResolveSandbox = func(string) (sandbox.Isolator, error) { return namedJail("bwrap"), nil }
	t.Cleanup(func() { doctorResolveSandbox = orig })
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")

	var out, errb bytes.Buffer
	code := runDoctor([]string{"--repo", root, "--code", filepath.Join(root, "Sources", "A", "B.swift"),
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off"}, &out, &errb)
	got := out.String()
	if code != 1 {
		t.Errorf("exit %d, want 1:\n%s%s", code, got, errb.String())
	}
	if strings.Contains(got, "All checks passed") {
		t.Errorf("doctor passed a Swift file it cannot check:\n%s", got)
	}
	for _, want := range []string{"[ok  ] sandbox starts", "[FAIL] toolchain reachable inside the sandbox", swiftRefusal(t), flattenDetail(doctorWorkspaceNote)} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, got)
		}
	}
}

// On a full pass doctor lists what it does not check, and the workspace
// note comes after that list, so "reports the first" still names the list's
// first item.
func TestDoctorAllPassedListKeepsItsOrder(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(root, "a_test.go"), "package a\n")
	orig := doctorResolveSandbox
	doctorResolveSandbox = func(string) (sandbox.Isolator, error) { return namedJail("bwrap"), nil }
	t.Cleanup(func() { doctorResolveSandbox = orig })
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")

	var out, errb bytes.Buffer
	code := runDoctor([]string{"--repo", root, "--code", filepath.Join(root, "a.go"),
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off"}, &out, &errb)
	got := out.String()
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s%s", code, got, errb.String())
	}
	first := strings.Index(got, "reports the first as COULD-NOT-GRADE")
	note := strings.Index(got, flattenDetail(doctorWorkspaceNote))
	if first < 0 || note < 0 || note < first {
		t.Errorf("the workspace note must follow the not-checked list and its COULD-NOT-GRADE line:\n%s", got)
	}
}

// Round-2 review: a Swift package whose only test is named for behaviour, so
// no file pairs by name and there is no candidate. The evidence pass still
// runs, over every enumerated source, in Swift: the advice must come from
// that language too, not fall back to the bwrap block.
func TestCertifyRepoSandboxRefusalCountsTheEvidencePassLanguage(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Package.swift"), "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"A\")\n")
	mustWrite(t, filepath.Join(root, "Sources", "A", "B.swift"), "public func b() -> Int { 1 }\n")
	mustWrite(t, filepath.Join(root, "Tests", "ATests", "BehaviourTests.swift"), "import XCTest\n@testable import A\nfinal class BehaviourTests: XCTestCase { func testB() { XCTAssertEqual(b(), 1) } }\n")
	failSandbox(t)

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--no-selection-cache", "--", "swift", "test"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got := errb.String()
	if !strings.Contains(got, swiftRefusal(t)) {
		t.Errorf("stderr lacks Swift's refusal:\n%s", got)
	}
	for _, never := range []string{"--jail container", "apparmor_parser"} {
		if strings.Contains(got, never) {
			t.Errorf("the bwrap block came back for a Swift-only evidence run (%q):\n%s", never, got)
		}
	}
}

// The empty case, a stated choice: a run with no candidate and no language
// for an evidence pass has no language to speak for, so the door gives its
// sandbox advice unchanged — the only thing that can be said about a
// sandbox that will not start.
func TestCertifyRepoSandboxRefusalWithNoLanguageKeepsTheAdvice(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "README.md"), "nothing to audit\n")
	failSandbox(t)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--no-selection-cache"}, &out, &errb)
	want := "corral certify --repo: " + bwrapUnavailableError(errors.New(`bwrap backend unavailable: bwrap not found on PATH`), true).Error() + "\n"
	if code != 1 || errb.String() != want {
		t.Errorf("exit %d, stderr:\n%q\nwant exit 1 and the advice unchanged:\n%q", code, errb.String(), want)
	}
}

// startSandbox makes every sandbox resolution succeed, as a host whose bwrap
// works does: the seam the doors below see, with no sandbox run.
func startSandbox(t *testing.T) {
	t.Helper()
	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) { return namedJail("bwrap"), nil }
	t.Cleanup(func() { resolveJailFn = orig })
}

// Final review, finding 1: with a sandbox that STARTS, a repository whose
// every gradable language refuses every sandbox used to pass the preflight
// (it asked the languages only when the sandbox failed), run the evidence
// pass in a sandbox Swift cannot use, derive (pay for) a goal per file, and
// only then refuse each file. The preflight now refuses the run first, with
// the language's own refusal, before the model preflight is even reached.
func TestCertifyRepoWithAWorkingSandboxRefusesALanguageTheSandboxNeverRuns(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	startSandbox(t)

	for name, args := range map[string][]string{
		"real run": {"--repo", root, "--derive-model", testHerdWriter, "--writer-model", testHerdWriter,
			"--mutant-model", testHerdMutant, "--critic-model", "off", "--no-ledger", "--no-selection-cache", "--no-goal-cache", "--", "swift", "test"},
		"dry run with --evidence": {"--repo", root, "--dry-run", "--evidence", "--no-selection-cache", "--", "swift", "test"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := runCertifyRepo(args, &out, &errb)
			if code != 1 {
				t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
			}
			got := errb.String()
			if !strings.Contains(got, swiftRefusal(t)) {
				t.Errorf("stderr lacks Swift's refusal:\n%s", got)
			}
			for _, never := range []string{"--jail container", "apparmor_parser", "goals derived"} {
				if strings.Contains(got+out.String(), never) {
					t.Errorf("output has %q, which a run refused at its preflight never reaches:\n%s%s", never, out.String(), got)
				}
			}
		})
	}
}

// The mixed case with a working sandbox: the Go files can run, so the run is
// not refused, and it says, before anything is spent, that the sandbox never
// runs its Swift files.
func TestCertifyRepoWithAWorkingSandboxStatesTheMixedRefusal(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	ex := newLocalExecutor(root, nil, substrateJail, 0, nil)
	defer ex.Close()
	var out bytes.Buffer
	ex.progress = &out
	ex.jailErr = nil
	if err := ex.preflight([]string{"go", "swift"}); err != nil {
		t.Fatalf("a mixed run was refused: %v", err)
	}
	note := "corral's sandbox never runs this run's Swift files, on any host:\n" + swiftRefusal(t)
	if !strings.Contains(out.String(), note) {
		t.Errorf("progress lacks the mixed note %q:\n%s", note, out.String())
	}
	// The workspace substrate runs no sandbox, so it says nothing of one.
	ws := newLocalExecutor(root, nil, substrateWorkspace, 0, nil)
	defer ws.Close()
	var wout bytes.Buffer
	ws.progress = &wout
	if err := ws.preflight([]string{"swift"}); err != nil || wout.Len() != 0 {
		t.Errorf("workspace substrate: err=%v progress=%q, want neither", err, wout.String())
	}
}

// `corral tests map` asks the same preflight with a working sandbox too.
func TestTestsMapWithAWorkingSandboxRefusesALanguageTheSandboxNeverRuns(t *testing.T) {
	root := t.TempDir()
	writeSwiftPackage(t, root)
	startSandbox(t)

	var out, errb bytes.Buffer
	code := runTests([]string{"map", "--repo-dir", root, "--workers", "1", "--no-selection-cache", "--out", filepath.Join(t.TempDir(), "m.json"), "--", "swift", "test"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), swiftRefusal(t)) {
		t.Errorf("exit %d, want 1 with Swift's refusal; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
}
