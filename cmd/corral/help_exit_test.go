// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"list", "-h"}, {"list", "help"}} {
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

// A leaf flag's -h anywhere among the leaf's flags is the flag package's to
// answer, and answering it must not need a brain or a token: the dispatch used
// to read only the first two arguments, saw `list --json`, and demanded a brain
// before the parse ever ran.
func TestMatrixLeafFlagHelpNeedsNoBrain(t *testing.T) {
	// brainToken reads the keystore; keep it empty and private to the test.
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CORRALAI_BRAIN_TOKEN", "")
	envs := map[string]func(string) string{
		"unset": func(string) string { return "" },
		"set-no-token": func(k string) string {
			if k == "CORRAL_BRAIN" {
				return "http://127.0.0.1:9"
			}
			return ""
		},
	}
	for name, env := range envs {
		for _, args := range [][]string{{"list", "--json", "-h"}, {"list", "-json", "--help"}} {
			var out, errb bytes.Buffer
			code := runMatrixCommand(args, env, &out, &errb)
			all := out.String() + errb.String()
			if code != 0 {
				t.Errorf("[%s] matrix %v exited %d, want 0:\n%s", name, args, code, all)
			}
			if !strings.Contains(all, "-json") {
				t.Errorf("[%s] matrix %v did not document -json:\n%s", name, args, all)
			}
			if strings.Contains(all, "CORRAL_BRAIN") || strings.Contains(all, "BRAIN_TOKEN") {
				t.Errorf("[%s] matrix %v answered help with a brain error:\n%s", name, args, all)
			}
		}
	}
}

// `list -- -h` is NOT a help request (-h after `--` is a positional), so with
// no brain it is refused like any real invocation. What it must never do is
// reach runMatrix with a nil reader and panic, which it did.
func TestMatrixDashDashHelpDoesNotPanic(t *testing.T) {
	var out, errb bytes.Buffer
	code := runMatrixCommand([]string{"list", "--", "-h"}, func(string) string { return "" }, &out, &errb)
	if code == 0 {
		t.Errorf("exit 0 for a non-help invocation with no brain:\n%s%s", out.String(), errb.String())
	}
	// Called directly with a nil reader, as the old dispatch did.
	for _, a := range [][]string{{"list", "help"}, {"list", "-h"}} {
		if code := runMatrix(a, nil, &out, &errb); code != 0 {
			t.Errorf("runMatrix(%v, nil) = %d, want 0", a, code)
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
	// `list -h` printed the stored secret NAMES instead of usage.
	var lout bytes.Buffer
	if err := runSecret([]string{"list", "-h"}, tripwireReader{t}, &lout); err != nil || !strings.Contains(lout.String(), "usage: corral secret list") {
		t.Errorf("secret list -h = %v, %q; want usage", err, lout.String())
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
	if verbWantsHelp([]string{"confirm", "42:5", "--why", "help"}) {
		t.Fatal("a --why value was read as a help token")
	}
	for _, a := range [][]string{{"-h"}, {"list", "-h"}, {"show", "--help"}, {"confirm", "-h"}} {
		if !verbWantsHelp(a) {
			t.Errorf("%v not recognised as help", a)
		}
	}
}

// TestGenCLIDocsRefusesAFailedHelp drives the REAL scripts/gen-cli-docs.sh
// against a stub binary whose -h exits 2 and prints an error. refuse_failed_help
// had no test that it fires; the generator would otherwise have published the
// error as the binary's flag reference (it did, for matrix and secret).
//
// The script cd's to its own parent's parent and derives its binaries from
// cmd/*/, so the test builds a tiny throwaway module holding a copy of the
// script and one stub command: no function is sourced out of the script, the
// whole capture-and-refuse path runs as shipped. The control (-h exits 0)
// proves the refusal is caused by the exit status, not by the stub's shape.
func TestGenCLIDocsRefusesAFailedHelp(t *testing.T) {
	script, err := os.ReadFile("../../scripts/gen-cli-docs.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(helpExit int) (string, int) {
		root := t.TempDir()
		write := func(rel, body string, mode os.FileMode) {
			p := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), mode); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module stubmod\n\ngo 1.21\n", 0o600)
		write("scripts/gen-cli-docs.sh", string(script), 0o700)
		write("cmd/stub/main.go", fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "stub: simulated help")
	os.Exit(%d)
}
`, helpExit), 0o600)
		cmd := exec.Command("bash", "scripts/gen-cli-docs.sh")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		code := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}

	out, code := run(2)
	if code == 0 {
		t.Fatalf("gen-cli-docs.sh exited 0 although the binary's -h exited 2:\n%s", out)
	}
	if !strings.Contains(out, "exited non-zero") {
		t.Errorf("no refusal message in the output:\n%s", out)
	}
	if out, code := run(0); code != 0 {
		t.Fatalf("control: a -h that exits 0 was refused (code %d):\n%s", code, out)
	}
}
