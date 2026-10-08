// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

// TestMatrixHelpNeedsNoBrain pins the dispatch, not the flag set: `corral
// matrix -h` read CORRAL_BRAIN before it looked at its arguments, so on a
// machine with no brain the one request that cannot depend on state was
// answered with "set CORRAL_BRAIN". scripts/gen-cli-docs.sh captured that
// sentence and PUBLISHED it as matrix's flag reference.
func TestMatrixHelpNeedsNoBrain(t *testing.T) {
	noBrain := func(string) string { return "" }
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"list", "-h"}} {
		var out, errb bytes.Buffer
		code := runMatrixCommand(args, noBrain, &out, &errb)
		all := out.String() + errb.String()
		if code != 0 {
			t.Errorf("matrix %v exited %d, want 0 — asking for help is not a failure:\n%s", args, code, all)
		}
		if !strings.Contains(all, "usage") && !strings.Contains(all, "Usage") {
			t.Errorf("matrix %v printed no usage:\n%s", args, all)
		}
		if strings.Contains(all, "CORRAL_BRAIN") {
			t.Errorf("matrix %v answered a help request with the missing-brain error:\n%s", args, all)
		}
	}
}

// A real invocation without a brain must still fail, and say why: the guard
// above may only swallow help requests.
func TestMatrixListStillNeedsBrain(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runMatrixCommand([]string{"list"}, func(string) string { return "" }, &out, &errb); code == 0 {
		t.Fatal("matrix list with no brain exited 0")
	}
	if !strings.Contains(errb.String(), "CORRAL_BRAIN") {
		t.Errorf("missing-brain error lost: %q", errb.String())
	}
}

// flagParseExit is the one place that decides what a failed flag.Parse means.
// -h makes the flag package return ErrHelp after printing usage; that is a
// successful answer, and ~25 subcommands returned 2 for it.
func TestFlagParseExit(t *testing.T) {
	if got := flagParseExit(flag.ErrHelp); got != 0 {
		t.Errorf("flagParseExit(ErrHelp) = %d, want 0", got)
	}
	if got := flagParseExit(errors.New("flag provided but not defined: -x")); got != 2 {
		t.Errorf("flagParseExit(bad flag) = %d, want 2", got)
	}
}

func TestSecretAndControlHelpAreUsageNotErrors(t *testing.T) {
	for name, run := range map[string]func([]string, *bytes.Buffer) error{
		"secret":  func(a []string, o *bytes.Buffer) error { return runSecret(a, strings.NewReader(""), o) },
		"control": func(a []string, o *bytes.Buffer) error { return runControl(a, o) },
	} {
		var out bytes.Buffer
		if err := run([]string{"-h"}, &out); err != nil {
			t.Errorf("%s -h returned error %v", name, err)
		}
		if !strings.Contains(out.String(), "usage") && !strings.Contains(out.String(), "Usage") {
			t.Errorf("%s -h printed no usage on stdout: %q", name, out.String())
		}
	}
}

// `control seed -h` prints its flag set (to stderr, via the flag package) and
// must report success, not "flag: help requested".
func TestControlSeedHelpIsNotAnError(t *testing.T) {
	var out bytes.Buffer
	if err := runControl([]string{"seed", "-h"}, &out); err != nil {
		t.Errorf("control seed -h returned %v", err)
	}
}

// tripwireReader fails the test if anything reads stdin.
type tripwireReader struct{ t *testing.T }

func (r tripwireReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("secret read stdin on a help/bad-name request")
	return 0, io.EOF
}

// A help token after a leaf is a help request, and a name that begins with "-"
// is refused: neither may read stdin or touch the keystore. `secret set -h`
// used to store a secret named "-h".
func TestSecretLeafHelpAndDashNamesTouchNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORRAL_CREDS_DIR", dir)
	for _, leaf := range []string{"set", "get", "rm"} {
		var out bytes.Buffer
		if err := runSecret([]string{leaf, "-h"}, tripwireReader{t}, &out); err != nil {
			t.Errorf("secret %s -h returned %v, want a usage answer", leaf, err)
		}
		if !strings.Contains(out.String(), "usage: corral secret "+leaf) {
			t.Errorf("secret %s -h printed %q", leaf, out.String())
		}
		for _, name := range []string{"-x", "--force"} {
			if err := runSecret([]string{leaf, name}, tripwireReader{t}, &bytes.Buffer{}); err == nil {
				t.Errorf("secret %s %s was accepted; a name beginning with - is a mistyped flag", leaf, name)
			}
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("the keystore directory was written to: %v", ents)
	}
}

// `confirm X --why help` is an adjudication whose reason is the word "help".
// It must run (and here, fail loudly via the admin fake), never exit 0 as help.
func TestCriticScoreWhyHelpIsNotAHelpRequest(t *testing.T) {
	admin := &fakeCriticAdmin{adjErr: errBoom}
	var out, errOut bytes.Buffer
	if rc := runCriticScore([]string{"confirm", "42:5", "--why", "help"}, fakeCriticLister{}, admin, &out, &errOut); rc == 0 {
		t.Fatalf("exit 0 with nothing adjudicated:\n%s%s", out.String(), errOut.String())
	}
	if criticScoreWantsHelp([]string{"confirm", "42:5", "--why", "help"}) {
		t.Fatal("a --why value was read as a help token")
	}
	for _, a := range [][]string{{"-h"}, {"list", "-h"}, {"show", "--help"}, {"confirm", "-h"}} {
		if !criticScoreWantsHelp(a) {
			t.Errorf("%v not recognised as help", a)
		}
	}
}
