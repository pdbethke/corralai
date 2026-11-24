// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestOverlayWritesCodeAtItsPathAndLeavesBaseAlone(t *testing.T) {
	base := map[string]string{"a.go": "A", "b.go": "B"}
	o := Overlay{Base: base, CodePath: "a.go"}

	if got, want := o.With("X"), map[string]string{"a.go": "X", "b.go": "B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("With = %v, want %v", got, want)
	}
	if want := map[string]string{"a.go": "A", "b.go": "B"}; !reflect.DeepEqual(base, want) {
		t.Fatalf("Base was modified: %v", base)
	}

	o.Compose = func(c string) string { return c + "\n//t" }
	if got := o.With("X")["a.go"]; got != "X\n//t" {
		t.Fatalf("composed file = %q, want %q", got, "X\n//t")
	}
}

// TestNoWorkspaceMapIsAssembledOutsideOverlay is derived from the tree: any
// non-test .go file under internal/ and cmd/ that writes code into a map at
// the audited path by hand has bypassed Overlay, and a rule added to
// Overlay.Compose would then miss that run.
func TestNoWorkspaceMapIsAssembledOutsideOverlay(t *testing.T) {
	pat := regexp.MustCompile(`\[([A-Za-z_]+\.)*[cC]odePath\] = `)
	// The one exemption: mutantset.go's r.files is a record of
	// MutantSetEntry values keyed by path, not a workspace of file contents.
	exempt := filepath.Join("cmd", "corral", "mutantset.go")
	seen := 0
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(f string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
				return err
			}
			seen++
			if filepath.Base(f) == "overlay.go" || strings.HasSuffix(f, exempt) {
				return nil
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(b), "\n") {
				if pat.MatchString(line) {
					t.Errorf("%s:%d assembles a workspace by hand, use Overlay: %s", f, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen < 50 {
		t.Fatalf("walked only %d files; the tree walk is not finding the repo", seen)
	}
}

// inlineJail stands in for a compiler and a runner over p.go, hermetically:
// a run fails when the file at p.go does not PARSE as Go (go/parser, a real
// parse, so CanaryCode with a block after it is judged as source, not as a
// string), or when it holds the token KILL. It records what every run saw at
// p.go beside the command, in order.
type inlineJail struct {
	mu   sync.Mutex
	runs []inlineRun
}

type inlineRun struct {
	cmd  []string
	code string
}

func (j *inlineJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	code := files["p.go"]
	j.runs = append(j.runs, inlineRun{cmd: slices.Clone(cmd), code: code})
	if _, err := parser.ParseFile(token.NewFileSet(), "p.go", code, 0); err != nil {
		return false, nil
	}
	return !strings.Contains(code, "KILL"), nil
}

const (
	inlineCode  = "package p\n\nfunc F() int { return 1 }\n"
	inlineBlock = "\nfunc TestCorralInline(t *testing.T) {}\n"
)

func inlineMutants() []Mutant {
	return []Mutant{
		{ID: "m1", Search: "return 1", Replace: "return 2 /* KILL */"},
		{ID: "m2", Search: "return 1", Replace: "return 3"},
	}
}

// TestInlineTestRidesEveryRun: an authored test that lands IN the audited
// file is appended to whatever code is in place on every run of the Score —
// the baseline, the canary, each mutant (and its confirmation) and the
// fail-fast probe — because every run rewrites that file, and a run that
// lacked the block would grade a suite without the test it claims to grade.
func TestInlineTestRidesEveryRun(t *testing.T) {
	j := &inlineJail{}
	ff := func(cmd []string) ([]string, bool) { return append(slices.Clone(cmd), "-x"), true }
	rep, err := Score(context.Background(), j, map[string]string{"go.mod": "module p\n"}, "p.go", inlineCode,
		inlineMutants(), []string{"run"}, WithInlineTest(inlineBlock), WithMutantFailFast(ff))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || !rep.CanaryKilled {
		t.Fatalf("baseline/canary: %+v", rep)
	}
	seen := map[string]bool{}
	for i, r := range j.runs {
		if !strings.HasSuffix(r.code, inlineBlock) {
			t.Errorf("run %d (%v) did not carry the block at p.go:\n%s", i, r.cmd, r.code)
		}
		body := strings.TrimSuffix(r.code, inlineBlock)
		switch {
		case body == inlineCode && slices.Contains(r.cmd, "-x") && !seen["m1"] && !seen["m2"]:
			// Before any mutant: the probe, not a confirmation's rerun of
			// the compliant code with a mutant's command.
			seen["fail-fast probe"] = true
		case body == inlineCode:
			seen["baseline"] = true
		case body == CanaryCode+"\n":
			seen["canary"] = true
		case strings.Contains(body, "return 2"):
			seen["m1"] = true
		case strings.Contains(body, "return 3"):
			seen["m2"] = true
		}
	}
	for _, want := range []string{"baseline", "canary", "fail-fast probe", "m1", "m2"} {
		if !seen[want] {
			t.Errorf("no %s run was seen: %+v", want, j.runs)
		}
	}
	if !slices.Contains(rep.Killed, "m1") || !slices.Contains(rep.Survived, "m2") {
		t.Errorf("killed %v survived %v, want m1 killed and m2 survived", rep.Killed, rep.Survived)
	}
}

// TestInlineCanaryStillFails: the canary is the code in place, so the block
// is appended to it too; the file must still fail to parse, or an inline
// run's canary would be blinded by the very test it is checking.
func TestInlineCanaryStillFails(t *testing.T) {
	j := &inlineJail{}
	rep, err := Score(context.Background(), j, nil, "p.go", inlineCode, inlineMutants(), []string{"run"}, WithInlineTest(inlineBlock))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CanaryKilled {
		t.Fatalf("the canary with the block appended passed: %+v", rep)
	}
	var canary bool
	for _, r := range j.runs {
		canary = canary || r.code == CanaryCode+"\n"+inlineBlock
	}
	if !canary {
		t.Fatalf("no run saw the canary with the block appended: %+v", j.runs)
	}
}

func TestAppendBlockSeparatesOnlyWhenTheCodeLacksANewline(t *testing.T) {
	if got := AppendBlock("B\n")("A\n"); got != "A\nB\n" {
		t.Errorf("code ending in a newline: %q", got)
	}
	if got := AppendBlock("B\n")("A"); got != "A\nB\n" {
		t.Errorf("code without a final newline: %q", got)
	}
}
