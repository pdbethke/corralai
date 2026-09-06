// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestSubcommandDispatchedBeforeVersionScan locks the fix for the
// silent-green bug: `corral certify -- go test -v ./...` must dispatch to
// certify, NOT be diverted to showVersion by the -v inside the *checked
// command*'s own argv.
func TestSubcommandDispatchedBeforeVersionScan(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"certify", "--", "go", "test", "-v"}, "certify"},
		{[]string{"certify", "--brain", "x", "--", "true"}, "certify"},
		{[]string{"secret", "list"}, "secret"},
		{[]string{"secret", "--", "-v"}, "secret"},
		// No subcommand: os.Args[1] IS the flag itself — must fall through
		// to showVersion/showHelp, not be treated as a subcommand.
		{[]string{"-h"}, ""},
		{[]string{"--version"}, ""},
		{[]string{"version"}, ""},
		{[]string{}, ""},
		// An unknown word is handed back as itself: main refuses it BY
		// NAME (exit 2), which is the whole point — it used to be "" here,
		// and "" meant "start the server".
		{[]string{"bogus"}, "bogus"},
	}
	for _, c := range cases {
		if got := subcommand(c.args); got != c.want {
			t.Errorf("subcommand(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

// TestRunCertifyReachedDespiteVOrHelpInCheckedArgv proves the check still
// RUNS (report_build is called) even when the checked command's own argv
// (after --) contains -v/--help/version — those must never be scanned by
// showVersion/showHelp, since dispatch happens on os.Args[1] alone.
func TestRunCertifyReachedDespiteVOrHelpInCheckedArgv(t *testing.T) {
	for _, checkArgv := range [][]string{
		{"go", "test", "-v"},
		{"echo", "--help"},
		{"echo", "version"},
	} {
		run := &fakeRunner{exitCode: 0, output: []byte("ok\n")}
		post := &fakePoster{result: stubResult()}
		var stdout, stderr bytes.Buffer

		args := append([]string{
			"--brain", "https://brain.example",
			"--repo", "x", "--commit", "y", "--branch", "main",
			"--",
		}, checkArgv...)

		code := runCertify(args, run, post, nil, nil, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("runCertify(%v) = %d, want 0 (stderr=%s)", args, code, stderr.String())
		}
		if !post.called {
			t.Fatalf("runCertify(%v): the check must have run and been reported, but post was never called", args)
		}
	}
}

// TestVersionGoesToStdout: `corral version` wrote via log.Println, which goes
// to STDERR. So `corral version | grep`, `$(corral version)`, and every CI step
// that captures a version got an EMPTY string while the text scrolled past on
// the terminal looking perfectly fine — the failure is invisible exactly where
// someone would check for it.
//
// A version banner is the most likely thing anyone pipes, and `corral -h` two
// lines away in main() already prints to stdout. Stderr is for diagnostics; the
// answer to a question the user asked is stdout.
func TestVersionGoesToStdout(t *testing.T) {
	var out, errb bytes.Buffer
	printVersion(&out, &errb)

	if got := strings.TrimSpace(out.String()); got == "" {
		t.Fatalf("`corral version` wrote nothing to stdout — a piped or captured version is empty; stderr had %q", errb.String())
	}
	if !strings.Contains(out.String(), "corral") {
		t.Errorf("version line should name the binary; got %q", out.String())
	}
	if errb.Len() != 0 {
		t.Errorf("nothing should go to stderr for a successful version query; got %q", errb.String())
	}
}
