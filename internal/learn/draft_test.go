// SPDX-License-Identifier: Elastic-2.0

package learn

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubAsker struct {
	out string
	err error
}

func (a stubAsker) Ask(_ context.Context, _, _ string) (string, error) { return a.out, a.err }

func TestDraftFillsGuidanceAndSkill(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"go build failed: no go.mod"})
	reply := "GUIDANCE: In an empty workspace, run go mod init before building.\nSKILL-NAME: init-go-workspace\nSKILL:\n# init-go-workspace\nRun go mod init <module> before go build."
	if err := Draft(context.Background(), stubAsker{out: reply}, s, *p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByID(p.ID)
	if !strings.Contains(got.Guidance, "go mod init") || got.SkillName != "init-go-workspace" || !strings.Contains(got.SkillBody, "# init-go-workspace") {
		t.Fatalf("draft not stored: %+v", got)
	}
}

func TestDraftFailureLeavesProposalIntact(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("bug|x", "finding", "tester", []string{"e"})
	if err := Draft(context.Background(), stubAsker{err: errors.New("model down")}, s, *p); err == nil {
		t.Fatal("expected error surfaced")
	}
	got, _ := s.ByID(p.ID)
	if got.Status != StatusPending {
		t.Fatalf("proposal must remain pending: %+v", got)
	}
}
