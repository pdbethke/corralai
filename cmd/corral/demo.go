// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// `corral demo` is the two-minute first run.
//
// It exists because the honest first-run experience was not two minutes. Every
// other entry point asks the newcomer to supply a file, its paired test, a goal
// sentence worth writing, three model names, and a test command that runs green
// INSIDE a sandbox — and then to hope their project's dependencies are visible
// there. Auditing real third-party repositories took six attempts to produce one
// verdict, five of them lost to the environment: a database that was not up, a
// missing passwd entry, an editable install pointing at a sibling repo, a suite
// coupled to host paths, and a timeout.
//
// None of that is a fair first impression of what the tool DOES. So the demo
// removes every variable except the one worth seeing: it writes a tiny,
// self-contained Go package with a deliberately thin test, and audits it.
//
// It runs the REAL `certify --local` by building its argv and calling it — not a
// private code path. A demo that exercised its own shortcut could pass while the
// command every reader will type next is broken, which is the opposite of
// useful.
//
// It still has no default models. The demo is not an exception to the rule that
// corral never picks a vendor for you; it just makes everything else free.
func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "where to write the demo project (default: a new temp dir, printed and kept)")
	writer := fs.String("writer-model", "", "model for the test-writer role — REQUIRED, corral has no default models")
	mutant := fs.String("mutant-model", "", "model for the mutant-generator role — REQUIRED, corral has no default models")
	critic := fs.String("critic-model", "", `model for the test-critic role, which must differ from the writer's ("off" disables it)`)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if strings.TrimSpace(*writer) == "" || strings.TrimSpace(*mutant) == "" {
		fmt.Fprint(stderr, demoUsage)
		return 2
	}

	root := strings.TrimSpace(*dir)
	if root == "" {
		d, err := os.MkdirTemp("", "corral-demo-")
		if err != nil {
			fmt.Fprintf(stderr, "corral demo: creating a temp project: %v\n", err)
			return 1
		}
		root = d
	}
	if err := writeDemoProject(root); err != nil {
		fmt.Fprintf(stderr, "corral demo: writing the demo project: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "demo project written to %s\n", root)
	fmt.Fprintf(stdout, "  passwd.go       a password rule with five clauses\n")
	fmt.Fprintf(stdout, "  passwd_test.go  a test that only checks two of them\n\n")
	fmt.Fprintf(stdout, "auditing it with your own test command — nothing here is mocked:\n")
	fmt.Fprintf(stdout, "  corral certify --local --code passwd.go --test passwd_test.go -- go test ./...\n\n")

	// The real command, reached the way a reader will reach it. --repo-dir is
	// the demo project so the package compiles as a package; the goal is the
	// contract the test is thin against.
	argv := []string{
		"--code", filepath.Join(root, "passwd.go"),
		"--test", filepath.Join(root, "passwd_test.go"),
		"--repo-dir", root,
		"--goal", demoGoal,
		"--writer-model", *writer,
		"--mutant-model", *mutant,
	}
	if c := strings.TrimSpace(*critic); c != "" {
		argv = append(argv, "--critic-model", c)
	}
	argv = append(argv, "--", "go", "test", "./...")

	code := runCertifyLocal(argv, stdout, stderr)

	fmt.Fprintf(stdout, "\nthe demo project is still at %s — read passwd_test.go and see what it never asserts.\n", root)
	fmt.Fprintf(stdout, "to try this on your own code, run `corral doctor` first: it checks the environment for free,\n")
	fmt.Fprintf(stdout, "and the environment is what stops an audit far more often than the tests are.\n")
	return code
}

// demoGoal is the contract the demo's test is deliberately thin against. It is
// written the way a good goal is written — as the guarantee, clause by clause,
// not as a description of the code — because the goal is what the mutants are
// generated to violate, and a vague goal produces vague mutants.
const demoGoal = "Valid reports true only when the password is at least 12 characters long AND contains an uppercase letter AND a lowercase letter AND a digit AND a symbol; any password failing any one of those five clauses must be rejected."

const demoUsage = `corral demo — a complete audit of a tiny project, in one command

corral has no default models, so name the seats you want (any provider you hold
a key for; the only rule is that the critic must differ from the writer):

  corral demo --writer-model <model> --mutant-model <model> --critic-model <model>

It writes a small Go package with a five-clause password rule and a test that
only checks two of them, then audits it with the real ` + "`certify --local`" + `. You
need a Go toolchain (you have one — you installed corral with it) and one
provider key. Nothing else: no venv, no database, no fixtures.

Flags:
  --writer-model   model for the test-writer role (required)
  --mutant-model   model for the mutant-generator role (required)
  --critic-model   model for the test-critic role ("off" disables it)
  --dir            where to write the project (default: a temp dir, kept)
`

// writeDemoProject writes the smallest project that can show the whole point:
// a rule with several clauses, and a test that covers only the obvious ones.
//
// Go on purpose. The reader installed corral with `go install`, so a Go
// toolchain is the one dependency they are guaranteed to have, and it is
// normally under /usr where the jail can see it. A Python demo would need an
// interpreter AND pytest visible inside the sandbox — which is precisely the
// class of environment problem this command exists to route around.
func writeDemoProject(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"go.mod":         "module corraldemo\n\ngo 1.21\n",
		"passwd.go":      demoSource,
		"passwd_test.go": demoTest,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

const demoSource = `package corraldemo

import "unicode"

// Valid reports whether pw satisfies every clause of the password rule:
// at least 12 characters, and at least one uppercase letter, one lowercase
// letter, one digit and one symbol.
func Valid(pw string) bool {
	if len(pw) < 12 {
		return false
	}
	var upper, lower, digit, symbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			symbol = true
		}
	}
	return upper && lower && digit && symbol
}
`

// demoTest is thin ON PURPOSE, and thin the way real tests are thin: it covers
// the happy path and one obvious rejection, and never checks the clauses in
// between. It PASSES. That is the whole point — a green suite is not evidence
// that the rule is enforced, and this is what corral is for.
const demoTest = `package corraldemo

import "testing"

func TestValidAcceptsAGoodPassword(t *testing.T) {
	if !Valid("Str0ng!Passw0rd") {
		t.Fatal("a password meeting every clause must be accepted")
	}
}

func TestValidRejectsAShortPassword(t *testing.T) {
	if Valid("Sh0rt!") {
		t.Fatal("a password under 12 characters must be rejected")
	}
}
`
