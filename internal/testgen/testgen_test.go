// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/repoindex"
)

// fakeLLM records the system/user prompts it was asked and returns a canned
// response — no live model in these tests (spikes already proved output quality;
// this package proves prompt construction and response parsing).
type fakeLLM struct {
	resp               string
	err                error
	gotSystem, gotUser string
}

func (f *fakeLLM) Ask(ctx context.Context, system, user string) (string, error) {
	f.gotSystem, f.gotUser = system, user
	return f.resp, f.err
}

func TestWriteTest(t *testing.T) {
	f := &fakeLLM{resp: "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"}
	sigs := []repoindex.Signature{{Name: "ValidatePassword", Kind: "func", Params: []repoindex.Param{{Name: "pw", Type: "string"}}, Results: []string{"error"}, Exported: true}}
	out, err := WriteTest(context.Background(), f, "passwords >= 12 chars", "package target\nfunc ValidatePassword(pw string) error { return nil }", sigs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func TestGoal") || strings.Contains(out, "```") {
		t.Fatalf("unexpected test output: %q", out)
	}
	// prompt construction: the user prompt must carry the goal, the code, and the signature JSON.
	for _, want := range []string{"passwords >= 12 chars", "ValidatePassword", `"Name":"ValidatePassword"`} {
		if !strings.Contains(f.gotUser, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, f.gotUser)
		}
	}
}

func TestWriteTestEmptyResponseErrors(t *testing.T) {
	if _, err := WriteTest(context.Background(), &fakeLLM{resp: "   "}, "g", "c", nil); err == nil {
		t.Fatal("empty model response must error")
	}
}
