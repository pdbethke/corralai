// SPDX-License-Identifier: Elastic-2.0

package authoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// fakeJail is a deterministic stand-in for adequacy.Jail: it reports
// "compiles" for mutant code matching compileOK, and records the last
// command it was asked to run so tests can assert the build command (not
// some other command) was used.
type fakeJail struct {
	compileOK func(code string) bool
	lastCmd   []string
	err       error
}

func (fj *fakeJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	fj.lastCmd = testCmd
	if fj.err != nil {
		return false, fj.err
	}
	return fj.compileOK(files["code.go"]), nil
}

func TestCompileVerify(t *testing.T) {
	// fake jail: RunTest returns "compiles" (true) for mutant code containing "OK",
	// and false for code containing "BAD" (a stand-in for a build failure).
	fj := &fakeJail{compileOK: func(code string) bool { return strings.Contains(code, "OK") }}
	muts := []adequacy.Mutant{{ID: "m1", Code: "OK-1"}, {ID: "m2", Code: "BAD-2"}, {ID: "m3", Code: "OK-3"}}
	valid, discarded, err := compileVerify(context.Background(), fj, map[string]string{"go.mod": "x"}, "code.go", muts, []string{"go", "build", "./"})
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 2 || valid[0].ID != "m1" || valid[1].ID != "m3" {
		t.Fatalf("valid = %+v, want [m1 m3]", valid)
	}
	if len(discarded) != 1 || discarded[0] != "m2" {
		t.Fatalf("discarded = %v, want [m2]", discarded)
	}
	// each verify ran BuildCmd against a workspace containing base + the mutant at codePath
	if fj.lastCmd[0] != "go" || fj.lastCmd[1] != "build" {
		t.Errorf("build cmd not used: %v", fj.lastCmd)
	}
}

func TestCompileVerify_JailError(t *testing.T) {
	fj := &fakeJail{compileOK: func(string) bool { return true }, err: errors.New("sandbox exploded")}
	muts := []adequacy.Mutant{{ID: "m1", Code: "OK-1"}}
	_, _, err := compileVerify(context.Background(), fj, map[string]string{"go.mod": "x"}, "code.go", muts, []string{"go", "build", "./"})
	if err == nil {
		t.Fatal("expected error to propagate from jail")
	}
}
