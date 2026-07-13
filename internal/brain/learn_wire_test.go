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
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// TestProposalApprovalFansOut is the human gate of the learning loop, end to
// end over MCP: a clustered proposal is drafted, list_proposals surfaces it,
// approve_proposal fans its guidance into memory AND its skill into both
// artifacts and memory, and reject_proposal on a second proposal records the
// reason without touching memory/artifacts at all.
func TestProposalApprovalFansOut(t *testing.T) {
	dir := t.TempDir()
	// approve_proposal fans guidance/skill into memory via mem.Add(targetDir="")
	// — give this test its own default-memory dir so it can't collide with
	// another test's writes to the shared CORRALAI_MEMORY_DIR within this run.
	t.Setenv("CORRALAI_MEMORY_DIR", filepath.Join(dir, "default-mem"))
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

// learnHarness builds a dev-mode (unauthenticated caller = admin) server with
// learn + memory + artifacts stores over in-memory transports, plus a seeded
// pending proposal with a drafted guidance + skill. Shared by the status-guard,
// retry-convergence, and flag tests.
func learnHarness(t *testing.T, skillName string) (*mcp.ClientSession, *learn.Store, *memory.Store, *artifacts.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	// approve_proposal fans guidance/skill into memory via mem.Add(targetDir="")
	// (internal/brain/learn.go), which resolves to CORRALAI_MEMORY_DIR. Give
	// this test its own dir so it can't see (or be seen by) entries another
	// test in this package wrote to ITS default dir in the same run — the
	// package TestMain only keeps writes off the real ~/.claude directory, it
	// doesn't isolate tests from each other.
	t.Setenv("CORRALAI_MEMORY_DIR", filepath.Join(dir, "default-mem"))
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

	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	body := ""
	if skillName != "" {
		body = "# " + skillName + "\nsteps"
	}
	if err := lstore.SetDraft(p.ID, "run go mod init first", skillName, body); err != nil {
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
	t.Cleanup(func() { sess.Close() })
	return sess, lstore, mstore, astore, p.ID
}

// TestApproveProposalStatusGuard: only a PENDING proposal may be approved or
// rejected. A second approve must refuse (IsError) WITHOUT re-running the
// fan-out — no new artifact revision, status unchanged — and reject of an
// already-decided proposal must refuse too. This is what makes a retried
// approve safe: once approved, the gate closes.
func TestApproveProposalStatusGuard(t *testing.T) {
	sess, lstore, _, astore, id := learnHarness(t, "init-go-workspace")
	ctx := context.Background()

	var ap approveProposalOut
	callTask(t, sess, "approve_proposal", map[string]any{"id": id}, &ap)
	if !ap.OK || astore.HeadRev() != 1 {
		t.Fatalf("first approve: ok=%v headRev=%d, want ok=true rev=1", ap.OK, astore.HeadRev())
	}

	// Second approve → refused, no mutation.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": id}})
	if err != nil {
		t.Fatalf("approve_proposal (second) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("second approve of an already-approved proposal was accepted; want refusal")
	}
	if astore.HeadRev() != 1 {
		t.Fatalf("second approve minted a new artifact revision: headRev=%d, want 1", astore.HeadRev())
	}
	got, err := lstore.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusApproved {
		t.Fatalf("status after refused re-approve = %q, want approved", got.Status)
	}

	// Reject of an approved proposal → refused, status unchanged.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "reject_proposal", Arguments: map[string]any{"id": id, "reason": "nah"}})
	if err != nil {
		t.Fatalf("reject_proposal (approved) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("reject of an already-approved proposal was accepted; want refusal")
	}
	got, _ = lstore.ByID(id)
	if got.Status != learn.StatusApproved || got.RejectReason != "" {
		t.Fatalf("reject of approved mutated the row: %+v", got)
	}
}

// TestRejectProposalStatusGuard: a rejected proposal can be neither re-rejected
// nor approved afterwards.
func TestRejectProposalStatusGuard(t *testing.T) {
	sess, lstore, _, astore, id := learnHarness(t, "init-go-workspace")
	ctx := context.Background()

	var rj okOut
	callTask(t, sess, "reject_proposal", map[string]any{"id": id, "reason": "noise"}, &rj)
	if !rj.OK {
		t.Fatal("first reject failed")
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "reject_proposal", Arguments: map[string]any{"id": id, "reason": "again"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("second reject was accepted; want refusal")
	}
	got, _ := lstore.ByID(id)
	if got.RejectReason != "noise" {
		t.Fatalf("second reject overwrote the reason: %q", got.RejectReason)
	}

	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": id}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("approve of a rejected proposal was accepted; want refusal")
	}
	if astore.HeadRev() != 0 {
		t.Fatalf("approve of rejected wrote an artifact: headRev=%d", astore.HeadRev())
	}
	got, _ = lstore.ByID(id)
	if got.Status != learn.StatusRejected {
		t.Fatalf("status = %q, want rejected", got.Status)
	}
}

// TestApproveProposalRetryConverges simulates a prior HALF-COMPLETED approve
// (the skill artifact already landed, but the proposal never flipped to
// approved because a later step failed): re-approving must converge — succeed,
// mint NO duplicate artifact revision, and flip the status. This is the
// convergent-retry design: artifacts.Put keeps the current rev for
// byte-identical content and memory.Add is an upsert by slug, so re-running
// the fan-out is safe.
func TestApproveProposalRetryConverges(t *testing.T) {
	sess, lstore, _, astore, id := learnHarness(t, "init-go-workspace")

	// Simulate the prior half-completed approve: skill artifact already present.
	if _, _, err := astore.Put("skills/init-go-workspace/SKILL.md", []byte("# init-go-workspace\nsteps"), "operator", 0); err != nil {
		t.Fatal(err)
	}
	if astore.HeadRev() != 1 {
		t.Fatalf("pre-write headRev = %d, want 1", astore.HeadRev())
	}

	var ap approveProposalOut
	callTask(t, sess, "approve_proposal", map[string]any{"id": id}, &ap)
	if !ap.OK {
		t.Fatalf("re-approve after partial fan-out failed: %+v", ap)
	}
	if astore.HeadRev() != 1 {
		t.Fatalf("re-approve minted a duplicate revision: headRev=%d, want 1", astore.HeadRev())
	}
	if ap.SkillRev != 1 {
		t.Fatalf("skill_rev = %d, want 1 (the existing revision)", ap.SkillRev)
	}
	got, err := lstore.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}
}

// TestApproveProposalFlags covers the fan-out selectors: guidance_only skips
// the artifact write, skill_only skips the guidance promotion, and a proposal
// with no drafted skill promotes guidance only. Negative memory assertions use
// the handler's output fields (the guidance slug is only set by a successful
// mem.Add) plus the temp-scoped artifact store; searching the shared memory
// dir for ABSENCE would be flaky across runs.
func TestApproveProposalFlags(t *testing.T) {
	cases := []struct {
		name         string
		skillName    string
		args         map[string]any
		wantGuidance bool
		wantSkill    bool
	}{
		{"guidance_only skips the skill", "init-go-workspace", map[string]any{"guidance_only": true}, true, false},
		{"skill_only skips the guidance", "init-go-workspace", map[string]any{"skill_only": true}, false, true},
		{"empty SkillName promotes guidance only", "", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess, lstore, _, astore, id := learnHarness(t, tc.skillName)
			args := map[string]any{"id": id}
			for k, v := range tc.args {
				args[k] = v
			}
			var ap approveProposalOut
			callTask(t, sess, "approve_proposal", args, &ap)
			if !ap.OK {
				t.Fatalf("approve failed: %+v", ap)
			}
			if tc.wantGuidance && ap.PromotedGuidanceSlug == "" {
				t.Fatal("guidance was not promoted; want a promoted_guidance_slug")
			}
			if !tc.wantGuidance && ap.PromotedGuidanceSlug != "" {
				t.Fatalf("guidance was promoted (%q); want it skipped", ap.PromotedGuidanceSlug)
			}
			if tc.wantSkill {
				if ap.SkillPath == "" || ap.SkillRev != 1 || astore.HeadRev() != 1 {
					t.Fatalf("skill not promoted: path=%q rev=%d headRev=%d", ap.SkillPath, ap.SkillRev, astore.HeadRev())
				}
			} else {
				if ap.SkillPath != "" || astore.HeadRev() != 0 {
					t.Fatalf("skill was promoted: path=%q headRev=%d; want no artifact write", ap.SkillPath, astore.HeadRev())
				}
			}
			got, err := lstore.ByID(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != learn.StatusApproved {
				t.Fatalf("status = %q, want approved", got.Status)
			}
		})
	}
}

// TestApproveProposalRejectsTraversalSkillName reproduces review finding #3:
// ApproveProposal built "skills/"+p.SkillName+"/SKILL.md" straight from
// LLM-drafted output, so a drafted SkillName of "../hooks/pre-commit" would
// escape the skills/ namespace on a naive sync_pull client. The choke point
// in ApproveProposal must refuse loudly, leave the proposal pending (an
// operator can still reject it), and write NOTHING to the artifact store —
// while a normal valid name is unaffected.
func TestApproveProposalRejectsTraversalSkillName(t *testing.T) {
	sess, lstore, _, astore, id := learnHarness(t, "../hooks/pre-commit")

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": id}})
	if err != nil {
		t.Fatalf("approve_proposal call: %v", err)
	}
	if !res.IsError {
		t.Fatal("approve_proposal with a path-traversal skill name was accepted; want refusal")
	}

	got, err := lstore.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusPending {
		t.Fatalf("proposal status = %q, want pending (invalid skill name must not approve)", got.Status)
	}
	if astore.Count() != 0 || astore.HeadRev() != 0 {
		t.Fatalf("artifact store was written to (count=%d headRev=%d); want nothing written for an invalid skill name", astore.Count(), astore.HeadRev())
	}
}

// TestApproveProposalAcceptsValidSkillName is the positive twin of the
// traversal-name test above: a normal lowercase-hyphen skill name must
// promote exactly as before the finding #3 fix.
func TestApproveProposalAcceptsValidSkillName(t *testing.T) {
	sess, lstore, _, astore, id := learnHarness(t, "init-go-workspace")

	var ap approveProposalOut
	callTask(t, sess, "approve_proposal", map[string]any{"id": id}, &ap)
	if !ap.OK || ap.SkillPath == "" || astore.HeadRev() != 1 {
		t.Fatalf("valid skill name should promote cleanly: %+v headRev=%d", ap, astore.HeadRev())
	}
	got, err := lstore.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}
}

// TestRecurrenceAfterPromotionReopensProposal is the efficacy hook, end to
// end over MCP: a proposal for "missing-req|go.mod" is approved (promoted
// guidance is now standing), then two MORE report_finding calls of that same
// type+target arrive — evidence the promoted guidance did NOT stop the
// problem. One seed finding is written directly to the queue (the original
// evidence that justified the proposal in the first place) so the first of
// the two report_finding calls is itself already a recurrence (AddFinding
// flags "recurring" whenever a PRIOR finding shares type+target); the first
// report_finding call is thus the FIRST post-promotion recurrence (bumps the
// counter, does not reopen) and the second report_finding call is the
// SECOND post-promotion recurrence, which crosses the store's threshold of 2
// and reopens the proposal as a new pending revision with Supersedes set.
func TestRecurrenceAfterPromotionReopensProposal(t *testing.T) {
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
	qstore, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { qstore.Close() })
	telstore, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { telstore.Close() })

	// Seed + approve the proposal for this signature — the promoted guidance
	// that (per this test) failed to land.
	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lstore.Approve(p.ID); err != nil {
		t.Fatal(err)
	}

	// Original evidence finding — this is what the proposal was clustered
	// from, filed before the recurrence check in this test begins.
	if _, err := qstore.AddFinding(queue.Finding{
		MissionID: 1, Reporter: "seed", Type: "missing-req", Severity: "low", Target: "go.mod",
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Learn: lstore, Queue: qstore, Telemetry: telstore}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "operator", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	reportArgs := map[string]any{
		"name": "tester", "mission_id": int64(1), "type": "missing-req",
		"severity": "low", "target": "go.mod",
	}

	// First post-promotion recurrence: bumps the counter, does not reopen.
	var f1 findingOut
	callTask(t, sess, "report_finding", reportArgs, &f1)
	if f1.ID == 0 {
		t.Fatal("report_finding (1st) returned id=0")
	}
	ps, err := lstore.List("pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("after the FIRST post-promotion recurrence, want 0 pending proposals, got %d: %+v", len(ps), ps)
	}

	// Second post-promotion recurrence: crosses the threshold, reopens.
	var f2 findingOut
	callTask(t, sess, "report_finding", reportArgs, &f2)
	if f2.ID == 0 {
		t.Fatal("report_finding (2nd) returned id=0")
	}
	ps, err = lstore.List("pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("after the SECOND post-promotion recurrence, want 1 new pending proposal, got %d: %+v", len(ps), ps)
	}
	if ps[0].Supersedes != p.ID {
		t.Fatalf("reopened proposal supersedes = %d, want %d", ps[0].Supersedes, p.ID)
	}
	if ps[0].Signature != "missing-req|go.mod" {
		t.Fatalf("reopened proposal signature = %q, want missing-req|go.mod", ps[0].Signature)
	}

	// Telemetry carries the proposal_reopened event.
	rep, err := telstore.Query(`SELECT kind FROM events WHERE kind = 'proposal_reopened'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("want exactly 1 proposal_reopened telemetry event, got %d", len(rep.Rows))
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

// TestCreateMissionWeavesPromotedGuidanceCapped3 is the injection half of the
// learning loop, end to end over MCP: approve a proposal (promoting a
// guidance + skill entry into shared memory), seed three more shared entries
// directly (two lessons, one extra skill) so five vetted candidates exist,
// then create_mission must weave AT MOST 3 of them into any single phase
// instruction — priority lessons -> guidance -> skill — while the approved
// guidance ("run go mod init first") survives the cap because it is the only
// guidance candidate among the five.
func TestCreateMissionWeavesPromotedGuidanceCapped3(t *testing.T) {
	dir := t.TempDir()
	// This test's cap-of-3 assertion depends on an exact, deterministic
	// candidate count (2 lessons + 1 guidance + 2 skills). mem.Add(targetDir="")
	// resolves through CORRALAI_MEMORY_DIR, and Store.Build reindexes every
	// .md file present in that directory — so without a private dir here,
	// another test in this package that also writes to the default dir in
	// the same run (e.g. coordination_activity_test.go's "go-mod-init")
	// would silently add extra candidates and break the cap math.
	t.Setenv("CORRALAI_MEMORY_DIR", filepath.Join(dir, "default-mem"))
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
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	missions, err := mission.Open(filepath.Join(dir, "mi.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { missions.Close() })

	// One proposal, drafted with the guidance text the test looks for after
	// injection ("go mod init") and a skill mirror.
	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lstore.SetDraft(p.ID, "run go mod init first", "init-go-workspace", "# init-go-workspace\nsteps"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{
			Learn: lstore, Artifacts: astore, Queue: q, Missions: missions,
		}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "operator", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Approve #1: fans a guidance entry ("run go mod init first") and a skill
	// mirror ("init-go-workspace") into shared memory.
	var ap approveProposalOut
	callTask(t, sess, "approve_proposal", map[string]any{"id": p.ID}, &ap)
	if !ap.OK {
		t.Fatalf("approve_proposal not ok: %+v", ap)
	}

	// Three more promoted (shared) entries, added directly as an admin would
	// (via SetShared/approval in production) — two lessons + one extra
	// skill, all matching the directive below on the word "init" (the
	// underlying DuckDB FTS index doesn't tokenize 2-letter words like "go",
	// so every crafted entry below shares "init" with the directive to
	// guarantee a keyword match regardless of index backend). Combined with
	// the approval's guidance + skill, five vetted candidates now exist: 2
	// lessons, 1 guidance, 2 skills. Priority lessons -> guidance -> skill
	// fills the cap of 3 with BOTH lessons and the SOLE guidance entry
	// (2+1=3), dropping both skills — deterministic, since there is exactly
	// one guidance candidate so no ranking tie-break decides whether it
	// survives.
	if _, _, _, err := mstore.Add("lesson-init-a", "always run go build before committing during init", "always run go build before committing during init", "lesson", "default", "", true, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mstore.Add("lesson-init-b", "pin toolchain versions during init setup", "pin toolchain versions during init setup", "lesson", "default", "", true, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mstore.Add("extra-init-skill", "# extra-init-skill\nmore init steps", "skill: more init steps", "skill", "default", "", true, "admin"); err != nil {
		t.Fatal(err)
	}

	// Now 5 vetted candidates exist: 2 lessons, 1 guidance (from approval),
	// 2 skills (from approval + the extra one just added). Cap 3, priority
	// lesson -> guidance -> skill: both lessons (2) + the sole guidance
	// entry (1) = 3, both skills dropped. The approved guidance text
	// "go mod init" MUST survive — it is the only guidance candidate, so no
	// ranking tie-break decides whether it makes the cap.
	// The build-plan sizer (ScaledPlan) is retired: create_mission no longer
	// synthesizes a plan for an omitted one, so tests must supply an explicit
	// plan.
	var mv mission.MissionView
	callTask(t, sess, "create_mission", map[string]any{
		"directive": "init a new go project tool",
		"plan": []map[string]any{
			{"name": "build", "role": "builder", "instruction": "Init the go project tool."},
		},
	}, &mv)
	if mv.ID == 0 {
		t.Fatalf("create_mission returned no id: %+v", mv)
	}

	tasks, err := q.List(mv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("no tasks created for mission")
	}
	var sawGoModInit bool
	for _, task := range tasks {
		if n := strings.Count(task.Instruction, "(author:"); n > 3 {
			t.Fatalf("task %q instruction carries %d injected items, want <=3:\n%s", task.Key, n, task.Instruction)
		}
		if strings.Contains(task.Instruction, "go mod init") {
			sawGoModInit = true
		}
	}
	if !sawGoModInit {
		t.Fatal("no task instruction carries the approved guidance (\"go mod init\") — it should have survived the cap")
	}
}
