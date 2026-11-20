// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// swiftRepo is a SwiftPM package with one source and its test target, and a
// stand-in swift alone on PATH: everything Preflight checks is in place, so
// what refuses is the jail's half and nothing else.
func swiftRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Package.swift"), "// swift-tools-version:6.0\n")
	mustWrite(t, filepath.Join(root, "Sources", "Calc", "Calc.swift"), "public func add(_ a: Int, _ b: Int) -> Int { a + b }\n")
	mustWrite(t, filepath.Join(root, "Tests", "CalcTests", "CalcTests.swift"), "import Testing\n@testable import Calc\n\n@Test func adds() { #expect(add(2, 3) == 5) }\n")
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "swift"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "swift"), 0o700); err != nil { // #nosec G302 -- test stand-in must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return root
}

// TestSwiftIsRefusedOnlyWhereAJailRuns: corral's sandbox does not run
// Swift, so every jailed door (certify --local, certify --repo on the jail
// substrate, which both go through resolveAuditPlugin) refuses a Swift file
// before any jail, model or spend, naming the substrate that works; the
// workspace substrate, which runs the operator's toolchain in a checkout,
// admits it.
func TestSwiftIsRefusedOnlyWhereAJailRuns(t *testing.T) {
	root := swiftRepo(t)
	in := localAuditInput{repoDir: root, codePath: "Sources/Calc/Calc.swift", goal: "g", checkArgv: []string{"swift", "test"}}

	ws := in
	ws.substrate = substrateWorkspace
	if _, err := resolveAuditPlugin(ws); err != nil {
		t.Fatalf("--substrate workspace must admit a Swift file: %v", err)
	}
	for _, substrate := range []string{"", substrateJail} {
		jailed := in
		jailed.substrate = substrate
		_, err := resolveAuditPlugin(jailed)
		if err == nil {
			t.Fatalf("substrate %q builds a jail, which does not run Swift, and must refuse", substrate)
		}
		for _, want := range []string{"inside the audit jail", "--substrate workspace", "scratch clone"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("substrate %q: the refusal must name %q, got: %v", substrate, want, err)
			}
		}
	}
}

// TestDoctorRehearsesTheJailsSwiftRefusal: doctor's sandbox rehearsal asks
// the jail's half for the language --code names, so it says before any
// spend what certify --local would refuse.
func TestDoctorRehearsesTheJailsSwiftRefusal(t *testing.T) {
	swiftRepo(t)
	swift, _ := lang.ByName("swift")
	got := checkToolchain(nil, []string{"swift", "test"}, nil, swift)
	if got.ok || !strings.Contains(got.detail, "--substrate workspace") {
		t.Fatalf("doctor must fail the jail rehearsal for Swift, naming the substrate that works: %+v", got)
	}
}

// TestADryRunIsNotRefusedByTheJailsPreflight: --dry-run builds no sandbox
// (no executor, and so no jail preflight of any language), so a language
// whose jail half refuses is still enumerated and paired. Rust on a host
// whose jail cannot run its toolchain behaves the same way
// (TestARustDryRunIsNotRefusedByTheJailsPreflight, cgo-only beside the
// landingRepo it uses), which is what Swift matches.
func TestADryRunIsNotRefusedByTheJailsPreflight(t *testing.T) {
	t.Run("swift", func(t *testing.T) {
		root := swiftRepo(t)
		swift, _ := lang.ByName("swift")
		if lang.JailPreflight(swift, []string{"swift", "test"}) == nil {
			t.Fatal("precondition: the jail's half refuses Swift")
		}
		var out, errb bytes.Buffer
		if code := runCertifyRepo([]string{"--repo", root, "--dry-run"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		if !strings.Contains(out.String(), "Sources/Calc/Calc.swift") {
			t.Errorf("the dry run does not list the Swift source:\n%s", out.String())
		}
	})
}
