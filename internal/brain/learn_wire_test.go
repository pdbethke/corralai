// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/principals"
)

// TestProposalApprovalFansOut is the human gate of the learning loop, end to
// end over MCP: a clustered proposal is drafted, list_proposals surfaces it,
// approve_proposal fans its guidance into memory AND its skill into both
// artifacts and memory, and reject_proposal on a second proposal records the
// reason without touching memory/artifacts at all.
func TestProposalApprovalFansOut(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	astore, err := artifacts.Open(filepath.Join(dir, "a.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { astore.Close() })
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })

	// Seed a pending proposal with a drafted guidance + skill.
	p, created, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected a freshly created proposal")
	}
	if err := lstore.SetDraft(p.ID, "run go mod init first", "init-go-workspace", "# init-go-workspace\nsteps"); err != nil {
		t.Fatal(err)
	}

	// A second proposal, seeded for the reject path.
	p2, _, err := lstore.Upsert("missing-req|package.json", "finding", "builder", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{Learn: lstore, Artifacts: astore}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "operator", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// list_proposals with no filter defaults implicitly to "pending" scope via
	// explicit status — both seeded proposals are pending.
	var lp listProposalsOut
	callTask(t, sess, "list_proposals", map[string]any{"status": "pending"}, &lp)
	if len(lp.Proposals) != 2 {
		t.Fatalf("want 2 pending proposals, got %d: %+v", len(lp.Proposals), lp.Proposals)
	}

	// approve_proposal fans out: guidance into memory, skill into artifacts AND
	// memory, and marks the store row approved.
	var ap approveProposalOut
	callTask(t, sess, "approve_proposal", map[string]any{"id": p.ID}, &ap)
	if !ap.OK {
		t.Fatalf("approve_proposal not ok: %+v", ap)
	}
	if ap.PromotedGuidanceSlug == "" {
		t.Fatal("approve_proposal did not report a promoted_guidance_slug")
	}
	if ap.SkillPath != "skills/init-go-workspace/SKILL.md" {
		t.Fatalf("skill_path = %q, want skills/init-go-workspace/SKILL.md", ap.SkillPath)
	}
	if ap.SkillRev != 1 {
		t.Fatalf("skill_rev = %d, want 1 (first artifact write)", ap.SkillRev)
	}

	// Memory must carry BOTH the promoted guidance and the skill mirror.
	ghits, err := mstore.Search("run go mod init", "default", "", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if !anySlugContains(ghits, "guidance-builder-missing-req-go-mod") {
		t.Fatalf("memory missing promoted guidance entry, got %+v", ghits)
	}
	shits, err := mstore.Search("init-go-workspace", "default", "", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if !anySlugContains(shits, "init-go-workspace") {
		t.Fatalf("memory missing skill mirror entry, got %+v", shits)
	}

	// Artifacts must carry the skill body at head rev 1.
	if astore.HeadRev() != 1 {
		t.Fatalf("artifacts head rev = %d, want 1", astore.HeadRev())
	}
	art, err := astore.Get("skills/init-go-workspace/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if art == nil || string(art.Content) != "# init-go-workspace\nsteps" {
		t.Fatalf("artifacts skill body mismatch: %+v", art)
	}

	// The store row itself is approved.
	got, err := lstore.ByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusApproved {
		t.Fatalf("proposal status = %q, want approved", got.Status)
	}

	// reject_proposal on the second proposal: status + reason recorded, no
	// memory/artifacts fan-out.
	var rj okOut
	callTask(t, sess, "reject_proposal", map[string]any{"id": p2.ID, "reason": "not actionable"}, &rj)
	if !rj.OK {
		t.Fatalf("reject_proposal not ok: %+v", rj)
	}
	got2, err := lstore.ByID(p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != learn.StatusRejected {
		t.Fatalf("second proposal status = %q, want rejected", got2.Status)
	}
	if got2.RejectReason != "not actionable" {
		t.Fatalf("reject reason = %q, want %q", got2.RejectReason, "not actionable")
	}

	// list_proposals(pending) now shows none left.
	var lpAfter listProposalsOut
	callTask(t, sess, "list_proposals", map[string]any{"status": "pending"}, &lpAfter)
	if len(lpAfter.Proposals) != 0 {
		t.Fatalf("want 0 pending proposals after approve+reject, got %d", len(lpAfter.Proposals))
	}
}

// TestApproveProposalRequiresSuperuser proves the admin gate is not
// decorative: a Principals store with a real superuser seeded closes the gate
// to the unauthenticated (dev in-memory) caller, and approve/reject both
// refuse — mirroring TestPromoteReferenceRequiresAdmin's pattern.
func TestApproveProposalRequiresSuperuser(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Learn: lstore, Principals: pstore}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": p.ID}})
	if err != nil {
		t.Fatalf("approve_proposal call: %v", err)
	}
	if !res.IsError {
		t.Fatal("approve_proposal by a non-admin was accepted; want refusal")
	}

	res2, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "reject_proposal", Arguments: map[string]any{"id": p.ID}})
	if err != nil {
		t.Fatalf("reject_proposal call: %v", err)
	}
	if !res2.IsError {
		t.Fatal("reject_proposal by a non-admin was accepted; want refusal")
	}

	// Untouched: still pending.
	got, err := lstore.ByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusPending {
		t.Fatalf("proposal status = %q, want pending (gate must actually deny)", got.Status)
	}
}

func anySlugContains(hits []memory.Hit, want string) bool {
	for _, h := range hits {
		if strings.Contains(h.Slug, want) || h.Slug == want {
			return true
		}
	}
	return false
}
