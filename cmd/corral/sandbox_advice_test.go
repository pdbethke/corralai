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

// A repository with a language the sandbox runs AND one it never runs keeps
// the advice, which helps the first, and says the second would be refused
// even so.
func TestCertifyRepoSandboxRefusalForAMixedRepositoryKeepsTheAdviceAndAddsTheRefusal(t *testing.T) {
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
	for _, want := range []string{"--jail container", swiftRefusal(t)} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got)
		}
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
	for _, want := range []string{"[FAIL] sandbox starts", "no-such-backend", swiftRefusal(t), doctorWorkspaceNote} {
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
