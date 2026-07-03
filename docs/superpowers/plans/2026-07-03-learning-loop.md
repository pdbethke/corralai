# Learning Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recurring swarm lessons become human-approved, role-scoped guidance and fleet-synced skills, with repo-shipped seed docs — visibly, in the demo.

**Architecture:** A new `internal/learn` package holds the proposals store (SQLite), a deterministic recurrence sweep, and an LLM drafter. The brain runs the sweep on a ticker, exposes superuser-gated proposal tools over MCP, and fans approval out to the existing trust machinery (vetted memory + artifacts push). Mission creation injects promoted guidance; the demo surfaces the whole loop (Shep announcement, UI card, two-run arc).

**Tech Stack:** Go 1.26, modernc.org/sqlite (pure Go, matches queue store), existing `internal/llm` client for drafting, existing memory/artifacts stores for promotion.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-03-learning-loop-design.md` — read it first.
- **No auto-enforcement:** nothing shapes agent behavior without human approval (spec "Trust model").
- Lesson cluster threshold: **N≥3**; efficacy reopen threshold: **≥2 post-promotion recurrences**; injection cap: **≤3 items** per instruction.
- Corral metaphor in all user-facing strings (herd/corral, never bee/hive — memory `corralai-metaphor`).
- Every commit message ends with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`; repo-local git email is already the noreply address.
- All new code: `// SPDX-License-Identifier: Elastic-2.0` header, gosec-clean (annotate deliberate patterns with `// #nosec Gxxx -- reason`), `go vet` clean.
- House testing style: table-lite, `t.Fatalf` with got/want, clock seam via `var now = func() float64` where time matters.

---

### Task 1: `internal/learn` proposals store

**Files:**
- Create: `internal/learn/store.go`
- Test: `internal/learn/store_test.go`

**Interfaces:**
- Consumes: nothing (leaf package).
- Produces (later tasks rely on these exact names):
  - `learn.Open(path string) (*Store, error)`, `(*Store).Close() error`
  - `type Proposal struct { ID int64; Signature, Kind, Roles, Evidence, Guidance, SkillName, SkillBody, Status, RejectReason string; Count, RecurredAfter int; Supersedes int64; CreatedTS, UpdatedTS float64 }`
  - Statuses: `StatusPending`, `StatusApproved`, `StatusRejected` (= "pending","approved","rejected")
  - `(*Store).Upsert(signature, kind, roles string, evidence []string) (*Proposal, bool, error)` — dedup by signature: creates pending (returns created=true), or bumps Count/evidence on an existing pending one (created=false). A **rejected** signature is suppressed until the incoming cluster count is ≥ 2× its count at rejection, then it reopens as pending.
  - `(*Store).SetDraft(id int64, guidance, skillName, skillBody string) error`
  - `(*Store).List(status string) ([]Proposal, error)` (empty status = all), `(*Store).ByID(id int64) (*Proposal, error)`
  - `(*Store).Approve(id int64) (*Proposal, error)`, `(*Store).Reject(id int64, reason string) error`
  - `(*Store).RecordRecurrence(signature string) (reopened *Proposal, err error)` — increments RecurredAfter on the approved proposal for that signature; at ≥2 it clones a new pending proposal with `Supersedes` set and returns it (else nil).

- [ ] **Step 1: Write the failing tests**

```go
// SPDX-License-Identifier: Elastic-2.0

package learn

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "learn.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertDedupsBySignature(t *testing.T) {
	s := open(t)
	p, created, err := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"e1", "e2", "e3"})
	if err != nil || !created || p.Status != StatusPending || p.Count != 3 {
		t.Fatalf("first upsert: %+v created=%v err=%v", p, created, err)
	}
	p2, created2, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"e4"})
	if created2 || p2.ID != p.ID || p2.Count != 4 {
		t.Fatalf("second upsert should bump, not create: %+v created=%v", p2, created2)
	}
}

func TestRejectionSuppressesUntilDoubled(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("bug|build.sh", "finding", "builder", []string{"a", "b", "c"})
	if err := s.Reject(p.ID, "not actionable"); err != nil {
		t.Fatal(err)
	}
	// Same volume again: suppressed (no new pending proposal).
	if _, created, _ := s.Upsert("bug|build.sh", "finding", "builder", []string{"d"}); created {
		t.Fatal("rejected signature must stay suppressed below 2x")
	}
	// Cluster grows to 2x the count at rejection (3 -> 6): reopens.
	if _, created, _ := s.Upsert("bug|build.sh", "finding", "builder", []string{"e", "f", "g", "h", "i", "j"}); !created {
		t.Fatal("2x growth should reopen a rejected signature")
	}
}

func TestApproveAndEfficacyReopen(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"a", "b", "c"})
	if err := s.SetDraft(p.ID, "run go mod init first", "init-go-workspace", "# init\nsteps"); err != nil {
		t.Fatal(err)
	}
	ap, err := s.Approve(p.ID)
	if err != nil || ap.Status != StatusApproved || ap.Guidance == "" {
		t.Fatalf("approve: %+v err=%v", ap, err)
	}
	if r, _ := s.RecordRecurrence("missing-req|go.mod"); r != nil {
		t.Fatal("first post-promotion recurrence must not reopen")
	}
	r, err := s.RecordRecurrence("missing-req|go.mod")
	if err != nil || r == nil || r.Status != StatusPending || r.Supersedes != ap.ID {
		t.Fatalf("second recurrence should reopen as revision: %+v err=%v", r, err)
	}
	if r2, _ := s.RecordRecurrence("nope|nothing"); r2 != nil {
		t.Fatal("unknown signature must be a no-op")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/learn/ -count=1`
Expected: FAIL (package/functions undefined)

- [ ] **Step 3: Implement the store**

Schema and behavior (complete implementation in `store.go`; follow `internal/queue/store.go` idioms — `db.SetMaxOpenConns(1)`, WAL, `var now = func() float64` seam):

```go
const schema = `
CREATE TABLE IF NOT EXISTS proposals (
  id             INTEGER PRIMARY KEY,
  signature      TEXT NOT NULL,        -- e.g. "missing-req|go.mod" or "lesson|<cluster-slug>"
  kind           TEXT NOT NULL,        -- 'finding' | 'lesson'
  roles          TEXT NOT NULL DEFAULT '',
  evidence       TEXT NOT NULL DEFAULT '[]',   -- JSON array of strings, capped at 20
  count          INTEGER NOT NULL DEFAULT 0,   -- cluster size seen so far
  guidance       TEXT NOT NULL DEFAULT '',
  skill_name     TEXT NOT NULL DEFAULT '',
  skill_body     TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,                -- pending | approved | rejected
  reject_reason  TEXT NOT NULL DEFAULT '',
  rejected_count INTEGER NOT NULL DEFAULT 0,   -- count at rejection (suppression baseline)
  recurred_after INTEGER NOT NULL DEFAULT 0,
  supersedes     INTEGER NOT NULL DEFAULT 0,
  created_ts     REAL NOT NULL,
  updated_ts     REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_proposals_sig ON proposals(signature, status);`
```

Upsert logic: look for a `pending` row with the signature → bump count (+len(evidence)), append evidence (cap 20), return (row,false). Else look for a `rejected` row → if incoming total (its count + new evidence) < 2×rejected_count, bump its count and return (row,false) with no status change; otherwise insert a fresh pending row (count = old count + new) and return (row,true). Else insert pending (count = len(evidence)), return (row,true). `RecordRecurrence`: find latest `approved` row by signature; increment recurred_after; when it reaches 2, insert a pending clone (`supersedes` = approved id, guidance/skill copied as draft seeds, count 0) and reset recurred_after to 0.

- [ ] **Step 4: Run tests to green**

Run: `go test ./internal/learn/ -count=1` — Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/learn/ && git commit -m "feat(learn): proposals store — signature-deduped, rejection-suppressed, efficacy-reopening

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Deterministic sweep (recurrence detection + lesson clustering)

**Files:**
- Create: `internal/learn/sweep.go`
- Test: `internal/learn/sweep_test.go`

**Interfaces:**
- Consumes: `(*Store).Upsert` from Task 1.
- Produces:
  - `type FindingSignal struct { Type, Target, Role, Evidence string }`
  - `type LessonDoc struct { Name, Body, Author string }`
  - `Signature(f FindingSignal) string` → `f.Type + "|" + f.Target`
  - `ClusterLessons(docs []LessonDoc, minSize int) [][]LessonDoc` — pure function; token-Jaccard similarity ≥ 0.5 links two docs; connected groups of ≥ minSize are clusters. (Deterministic v1 of the spec's "near-duplicate lessons"; FTS/vector upgrades are spec-deferred.)
  - `(*Store).Sweep(findings []FindingSignal, lessons []LessonDoc) (opened []Proposal, err error)` — groups findings by Signature (≥3 occurrences → Upsert kind "finding", roles from signals), clusters lessons (≥3 → Upsert kind "lesson", signature `"lesson|" + slug(cluster[0].Name)`, evidence = names+bodies truncated).

- [ ] **Step 1: Write the failing tests**

```go
// SPDX-License-Identifier: Elastic-2.0

package learn

import "testing"

func TestSignature(t *testing.T) {
	got := Signature(FindingSignal{Type: "missing-req", Target: "go.mod"})
	if got != "missing-req|go.mod" {
		t.Fatalf("got %q", got)
	}
}

func TestClusterLessonsGroupsNearDuplicates(t *testing.T) {
	docs := []LessonDoc{
		{Name: "go-mod-init-1", Body: "empty workspace needs go mod init before go build can pass"},
		{Name: "go-mod-init-2", Body: "run go mod init first: an empty workspace fails go build without it"},
		{Name: "go-mod-init-3", Body: "go build fails in an empty workspace until go mod init runs"},
		{Name: "css-colors", Body: "prefer var(--fg) tokens over hex literals in the swarm ui"},
	}
	clusters := ClusterLessons(docs, 3)
	if len(clusters) != 1 || len(clusters[0]) != 3 {
		t.Fatalf("expected one 3-doc cluster, got %v", clusters)
	}
	if got := ClusterLessons(docs, 4); len(got) != 0 {
		t.Fatalf("minSize=4 should produce no clusters, got %v", got)
	}
}

func TestSweepOpensProposals(t *testing.T) {
	s := open(t)
	findings := []FindingSignal{
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "e1"},
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "e2"},
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "e3"},
		{Type: "bug", Target: "once.sh", Role: "tester", Evidence: "only twice"},
		{Type: "bug", Target: "once.sh", Role: "tester", Evidence: "still twice"},
	}
	opened, err := s.Sweep(findings, nil)
	if err != nil || len(opened) != 1 || opened[0].Signature != "missing-req|go.mod" {
		t.Fatalf("opened=%v err=%v", opened, err)
	}
	// Idempotent: same input opens nothing new (dedup bumps the pending row).
	opened2, _ := s.Sweep(findings, nil)
	if len(opened2) != 0 {
		t.Fatalf("re-sweep must not open duplicates: %v", opened2)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/learn/ -count=1` → FAIL

- [ ] **Step 3: Implement**

`ClusterLessons`: lowercase, split on non-alphanumerics, drop tokens <3 chars → set; Jaccard(a,b) = |∩|/|∪|; union-find over pairs ≥0.5; emit groups ≥minSize. `Sweep`: count findings per Signature; ≥3 → `Upsert(sig, "finding", role, evidences)`; collect only proposals where created=true into `opened`. Lesson clusters → `Upsert("lesson|"+slug, "lesson", "", evidence)` where slug is the first doc's Name lowercased with non-alphanumerics → `-`.

- [ ] **Step 4: Run to green** — `go test ./internal/learn/ -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/learn/ && git commit -m "feat(learn): deterministic sweep — finding signatures + token-Jaccard lesson clustering

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Drafter (LLM phrases, never decides)

**Files:**
- Create: `internal/learn/draft.go`
- Test: `internal/learn/draft_test.go`

**Interfaces:**
- Consumes: `Proposal`, `(*Store).SetDraft` (Task 1).
- Produces: `type Asker interface { Ask(ctx context.Context, system, user string) (string, error) }` (satisfied by `*llm.Client`); `Draft(ctx context.Context, a Asker, s *Store, p Proposal) error`.

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run to verify failure** → FAIL
- [ ] **Step 3: Implement**

System prompt (verbatim in code): `You distill recurring evidence from a herd of coding agents into reusable knowledge. Reply EXACTLY in this format:\nGUIDANCE: <one sentence of role-scoped guidance>\nSKILL-NAME: <kebab-case-slug or NONE>\nSKILL:\n<markdown skill body, or empty when NONE>`. User content: kind, signature, roles, evidence list. Parse with prefix scans (`GUIDANCE:`, `SKILL-NAME:`, everything after `SKILL:` line); `NONE` → empty skill fields; call `s.SetDraft`. The parser must tolerate missing SKILL sections (guidance-only proposals are valid).

- [ ] **Step 4: Run to green** → PASS
- [ ] **Step 5: Commit**

```bash
git add internal/learn/ && git commit -m "feat(learn): narrator-phrased proposal drafting with strict parse and graceful failure

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Brain wiring — Options, ticker, adapters

**Files:**
- Modify: `internal/brain/identity.go` (Options: add `Learn *learn.Store` and `LearnDrafter learn.Asker` fields, comment-documented like siblings)
- Modify: `cmd/corral/main.go` (open store, wire ticker)
- Test: compile + existing suites (wire behavior is tested in Task 5's MCP test)

**Interfaces:**
- Consumes: `learn.Open`, `(*learn.Store).Sweep`, `learn.Draft`, `queue.Store` findings listing, `memory.Store` lesson listing.
- Produces: brain Options fields `Learn`, `LearnDrafter`; env knobs `CORRALAI_LEARN_DB` (default `~/.claude/corralai_learn.sqlite3`), `CORRALAI_LEARN_SWEEP_SECONDS` (default 60).

- [ ] **Step 1: Add Options fields** (follow the `Queue`/`Telemetry` comment style at `internal/brain/identity.go:55-65`).

- [ ] **Step 2: Wire in `cmd/corral/main.go`** beside the reap ticker (~line 655): open the store; every sweep interval collect inputs and run:

```go
// learn sweep: deterministic recurrence detection feeding human-gated proposals.
go func() {
	t := time.NewTicker(time.Duration(envInt("CORRALAI_LEARN_SWEEP_SECONDS", 60)) * time.Second)
	defer t.Stop()
	for range t.C {
		fs, err := queueStore.AllFindings() // Task 4 adds this: SELECT type,target,reporter role?,evidence over all missions
		if err != nil { log.Printf("learn: findings: %v", err); continue }
		signals := make([]learn.FindingSignal, 0, len(fs))
		for _, f := range fs { signals = append(signals, learn.FindingSignal{Type: f.Type, Target: f.Target, Role: roleOfReporter(f), Evidence: f.Evidence}) }
		docs, _ := memStore.LessonsForLearning(200) // Task 4 adds: recent type=lesson entries (name, body, author)
		opened, err := learnStore.Sweep(signals, docs)
		if err != nil { log.Printf("learn: sweep: %v", err); continue }
		for _, p := range opened {
			log.Printf("learn: proposal #%d opened (%s, %d occurrences)", p.ID, p.Signature, p.Count)
			telRecord(telStore, "proposal_opened", p.Signature)
			if drafter != nil {
				pp := p
				if err := learn.Draft(ctx, drafter, learnStore, pp); err != nil { log.Printf("learn: draft #%d: %v", pp.ID, err) }
			}
		}
	}
}()
```

Add the two small accessor methods this needs — `queue.(*Store).AllFindings() ([]Finding, error)` (all missions, mirrors `Findings` without the mission filter) and `memory.(*Store).LessonsForLearning(limit int) ([]learn.LessonDoc-shaped, error)` returning name/body/author for `type=lesson` entries — **memory must not import learn**: return a local struct and convert at the call site. Include one unit test for each accessor in the owning package's test file.

- [ ] **Step 3: Build + full test** — `go build ./... && go test ./internal/queue/ ./internal/memory/ -count=1` → PASS
- [ ] **Step 4: Commit**

```bash
git add internal/brain/identity.go cmd/corral/main.go internal/queue/ internal/memory/ && git commit -m "feat(brain): learn store wiring — sweep ticker, accessors, telemetry

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: MCP proposal tools + approval fan-out (wire test)

**Files:**
- Create: `internal/brain/learn.go`
- Test: `internal/brain/learn_wire_test.go`
- Modify: `internal/brain/server.go` (register when `opts.Learn != nil`)

**Interfaces:**
- Consumes: Task 1 store; `memory.(*Store).Add` (signature: `Add(name, body, description, typ, project, targetDir string, shared bool, author string)`); `artifacts.(*Store).Put(path string, content []byte, by string, updatedTS float64)`; `opts.isAdmin` gate (identity.go:193).
- Produces MCP tools:
  - `list_proposals` `{status?}` → `{proposals: []}`
  - `approve_proposal` `{id, guidance_only?, skill_only?}` → `{ok, promoted_guidance_slug?, skill_path?, skill_rev?}` — superuser-gated
  - `reject_proposal` `{id, reason?}` → `{ok}` — superuser-gated

Approval fan-out (in the handler):
1. guidance (unless skill_only): `mem.Add("guidance-"+rolesSlug+"-"+sigSlug, p.Guidance, "promoted guidance ("+p.Signature+")", "guidance", "default", "", true, actorOf(req))`
2. skill (unless guidance_only, and SkillName != ""): `arts.Put("skills/"+p.SkillName+"/SKILL.md", []byte(p.SkillBody), actorOf(req), nowTS)` **and** mirror into memory: `mem.Add(p.SkillName, p.SkillBody, "skill: "+firstLine(p.SkillBody), "skill", "default", "", true, actorOf(req))`
3. `learnStore.Approve(id)`; telemetry `proposal_approved`.

- [ ] **Step 1: Write the failing wire test** (mirror `internal/brain/reclaim_wire_test.go` harness — in-memory transports, dev mode so the unauthenticated caller is admin):

```go
func TestProposalApprovalFansOut(t *testing.T) {
	// stores: learn + memory + artifacts (all t.TempDir()); NewServer with Options{Learn, Memory?, Artifacts}
	// seed: learnStore.Upsert("missing-req|go.mod","finding","builder",[]string{"a","b","c"})
	//        learnStore.SetDraft(id, "run go mod init first", "init-go-workspace", "# init-go-workspace\nsteps")
	// call list_proposals -> 1 pending
	// call approve_proposal {id} -> ok true
	// assert memory search finds "guidance-builder-missing-req-go-mod" AND "init-go-workspace"
	// assert artifacts head rev == 1 and Get("skills/init-go-workspace/SKILL.md") returns the body
	// assert learnStore.ByID(id).Status == approved
	// call reject on a second proposal -> status rejected, reason stored
}
```

Write it fully (follow the reclaim wire test's `call(tool, args)` helper); consult `internal/brain/memory_test.go` for how a memory-backed server is constructed and `internal/artifacts/store.go` for Get/head-rev accessors.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/brain/ -run Proposal -count=1` → FAIL
- [ ] **Step 3: Implement `internal/brain/learn.go`** — three `mcp.AddTool` registrations; admin gate: `if !opts.isAdmin(req) { return ..., fmt.Errorf("superuser required") }` on approve/reject; tool descriptions in corral voice (e.g. approve: "Promote a proposal the herd surfaced: its guidance joins vetted memory (shapes future instructions) and its skill syncs fleet-wide. Superuser only — the human gate of the learning loop.").
- [ ] **Step 4: Run to green** — `go test ./internal/brain/ -count=1` → PASS
- [ ] **Step 5: Commit**

```bash
git add internal/brain/ && git commit -m "feat(brain): proposal tools — list/approve/reject with human-gated fan-out to memory + artifacts

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Injection — promoted guidance + skill summaries into instructions

**Files:**
- Modify: `internal/brain/missions.go:75-90` (the RecallLessons weave)
- Modify: `internal/mission/lessons.go` (extend `InjectLessons` capping)
- Test: `internal/mission/lessons_test.go` additions; `internal/brain/learn_wire_test.go` extension

**Interfaces:**
- Consumes: `mem.RecallLessons(directive string, n int)` (existing); memory entries of `typ="guidance"` and `typ="skill"` created by Task 5.
- Produces: mission instructions that carry ≤3 injected items, formatted `LESSONS FROM THE HERD (vetted):\n- <text> — <author>`.

- [ ] **Step 1: Read first** — `internal/brain/missions.go:70-95` and `internal/mission/lessons.go` in full; confirm what `RecallLessons` filters on (it must return only promoted/vetted entries — if it doesn't already filter by status, add that filter in `internal/memory` with a unit test; this is the "VETTED only" invariant).
- [ ] **Step 2: Relax the demo gate.** Change the injection condition from `mem != nil && opts.Principals != nil` to `mem != nil`. Safety is preserved because RecallLessons returns only *promoted* entries, and promotion required the admin gate (in dev mode, the local operator — the demo's human — IS that gate; with auth on, a verified superuser is). Update the code comment to say exactly this.
- [ ] **Step 3: Failing test** — extend the Task 5 wire test: after approval, call `create_mission {directive:"build a Go tool"}` and assert some task instruction contains `go mod init` and that no instruction contains more than 3 injected items (craft 5 promoted entries; assert cap).
- [ ] **Step 4: Implement** — in the weave, also recall `guidance`/`skill` typed entries matching the directive, merge, cap at 3 (lessons first, then guidance, then skill one-liners `consult skill: <name> — <first line>`); extend `InjectLessons` with the cap parameter (keep the existing signature by capping before calling it, if simpler — decide by reading `lessons.go`).
- [ ] **Step 5: Green + commit**

```bash
git add internal/brain/missions.go internal/mission/ internal/memory/ && git commit -m "feat(missions): weave promoted guidance and skill pointers into instructions (cap 3, vetted only)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Efficacy hook — recurrence after promotion

**Files:**
- Modify: `internal/brain/tasks.go` (report_finding handler) or `internal/queue/findings.go` call site — wherever `AddFinding` returns `recurring=true`, notify learn.
- Test: extend `internal/brain/learn_wire_test.go`

**Interfaces:**
- Consumes: `(*learn.Store).RecordRecurrence(signature)` (Task 1); finding insert path.
- Produces: telemetry `proposal_reopened`; log line `learn: guidance for %s didn't land — revision proposal #%d opened`.

- [ ] **Step 1: Failing test** — wire test: approve a proposal for `missing-req|go.mod`; then `report_finding` twice with that type+target; assert a new pending proposal exists with `Supersedes` set.
- [ ] **Step 2: Implement** — in the brain's report_finding handler (find it in `internal/brain/tasks.go`; it already computes/marks recurring), after a recurring insert: `if opts.Learn != nil { if r, _ := opts.Learn.RecordRecurrence(f.Type+"|"+f.Target); r != nil { log + rec(tel, ..., "proposal_reopened", ...) } }`.
- [ ] **Step 3: Green** — `go test ./internal/brain/ -count=1` → PASS
- [ ] **Step 4: Commit**

```bash
git add internal/brain/ && git commit -m "feat(learn): efficacy loop — recurrence after promotion reopens the proposal as a revision

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: `corral-admin proposals` verbs

**Files:**
- Modify: `cmd/corral-admin/main.go` (new `proposals` command; mirror the `analyze` verb structure at :679 and the usage block at :826)

**Interfaces:**
- Consumes: the three MCP tools (Task 5).
- Produces CLI: `corral-admin proposals [list|show <id>|approve <id> [--guidance-only|--skill-only]|reject <id> --reason "..."]` — renders via the existing `printTable`/`okMsg` helpers; NULL-safe like the analyze fix ("-", not `<nil>`).

- [ ] **Step 1: Implement the verb** (list → table of id/signature/count/status/skill; show → full guidance + skill body; approve/reject → okMsg). Follow the `bind(fs)`/`parseFlags` conventions so flags work before or after positionals.
- [ ] **Step 2: Manual verification against a scratch brain**

```bash
go build -o /tmp/corral ./cmd/corral && go build -o /tmp/corral-admin ./cmd/corral-admin
HOME=$(mktemp -d) CORRALAI_ADDR=127.0.0.1:9023 /tmp/corral & sleep 2
CORRAL_BRAIN=http://127.0.0.1:9023 CORRAL_TOKEN=dev /tmp/corral-admin proposals list   # -> (0 rows)
```

Expected: clean empty table, no errors. Kill the brain.

- [ ] **Step 3: Commit**

```bash
git add cmd/corral-admin/ && git commit -m "feat(admin): proposals verbs — list/show/approve/reject

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: UI — Proposals card, /api/state block, Shep announcement

**Files:**
- Modify: `internal/ui/ui.go` (Deps gains `Learn *learn.Store`; `/api/state` gains `proposals`)
- Modify: `internal/ui/web/index.html` (card + approve/reject buttons)
- Modify: `cmd/corral-agent/scrum.go` (standup line when pending proposals appear)
- Modify: `cmd/corral/main.go` (pass Learn into ui.Deps)
- Test: `internal/ui/ui_test.go` addition (state includes proposals); `cmd/corral-agent/scrum_test.go` addition

**Interfaces:**
- Consumes: `(*learn.Store).List(learn.StatusPending)`; approve/reject via new POST endpoints `/api/proposal/approve` + `/api/proposal/reject` (UI is same-origin; handlers call the store fan-out — reuse the exact fan-out helper from Task 5 by exporting it as `brain.PromoteProposal(learn, mem, arts, id, actor) error`… **no**: keep UI thin — the UI handlers live in `internal/ui` and receive a `Promote func(id int64) error` + `Reject func(id int64, reason string) error` pair in Deps, wired in `cmd/corral/main.go` to the same fan-out logic factored into `internal/brain` as an exported function used by BOTH the MCP tool and the UI wiring.)
- Produces: `/api/state` → `"proposals": [{id, signature, count, guidance, skill_name, status}]`; UI card in the corral view (same `.tqueen`-card styling family as the review bar); Shep line `standup: … · 1 skill proposal awaiting the operator`.

- [ ] **Step 1: Factor the fan-out** — move Task 5's approval body into exported `func ApproveProposal(l *learn.Store, mem *memory.Store, arts *artifacts.Store, tel *telemetry.Store, id int64, actor string, guidanceOnly, skillOnly bool) (skillPath string, err error)` in `internal/brain/learn.go`; MCP handler calls it; rerun Task 5's test (must stay green).
- [ ] **Step 2: /api/state + endpoints + failing ui test** (assert a seeded pending proposal appears in the state JSON and that POST approve flips it).
- [ ] **Step 3: UI card** — in `index.html`, above the mission review bar: when `state.proposals` has pending items, render a card per proposal: signature, count badge, guidance text, skill name chip, Approve / Reject buttons POSTing to the endpoints; `skin()`-aware copy (`the herd proposes:` / matrix: `the construct proposes:` — reuse the existing skin vocabulary pattern with a `proposes` field per skin).
- [ ] **Step 4: Shep** — in `scrumFacts`, accept a `pendingProposals int` parameter (threaded from `/api/state` in `runScrumLoop`) and append `· N skill proposal(s) awaiting the operator` when >0; update `scrum_test.go` call sites and add one assertion.
- [ ] **Step 5: Green + visual check** — `go test ./internal/ui/ ./cmd/corral-agent/ -count=1` → PASS; scratch brain + browser screenshot of the card (seed one proposal via sqlite or a temporary admin call).
- [ ] **Step 6: Commit**

```bash
git add internal/ui/ internal/brain/ cmd/corral-agent/ cmd/corral/ && git commit -m "feat(ui): proposals card + state block; Shep announces pending proposals

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: Pre-seeding — CORRAL.md convention + corralai's own seed docs

**Files:**
- Create: `CORRAL.md` (repo root), `docs/corral/verify-gate.md`, `docs/corral/memory-etiquette.md`, `docs/corral/claims-and-leases.md`, `docs/corral/mission-lifecycle.md`, `docs/corral/demo-map.md`
- Modify: `cmd/corral-agent/repomirror.go` or the repo snapshot ingest path (read the spec's "at snapshot time" — find where a repo mission's snapshot lands, likely `internal/repo`/`internal/brain/reposync.go`) to ingest seed files as **advisory** memory entries tagged to the repo
- Modify: `deploy/demo/Dockerfile.brain` (COPY the seed docs into the image's memory dir alongside seed-memory)
- Modify: `CONTRIBUTING.md` (one paragraph inviting knowledge PRs to `docs/corral/`)
- Test: ingest unit test (seed files → advisory entries, hostile-repo content stays advisory)

**Content requirements for the seed docs** (each: title-as-slug, one topic, plain prose, ≤40 lines; write them from the codebase, not from imagination — cite file paths):
- `CORRAL.md`: what corralai is, where the herd's knowledge lives, pointer to docs/corral/, how a developer's agent queries it (`.mcp.json` → `search_memory`).
- `verify-gate.md`: gated tasks refuse completion without a recorded passing run; `report_execution` is how runs get recorded; supersede inherits gates.
- `memory-etiquette.md`: write lessons liberally; search before work; lessons are advisory until promoted; how promotion works.
- `claims-and-leases.md`: exclusive vs advisory claims, TTLs, the slacker rule, re-issue semantics.
- `mission-lifecycle.md`: directive → plan → queue → findings → reflex/lead re-planning → review gate → sprints.
- `demo-map.md`: the make targets, what each profile shows, where the UI tabs are.

- [ ] **Step 1: Write the six docs** (real content, verified against source).
- [ ] **Step 2: Ingest path + failing test** — on repo-mission snapshot, glob `CORRAL.md` + `docs/corral/*.md`, `mem.Add(name=slug, body, description=firstLine, typ="lesson", project=repoTag, shared=false /* advisory */, author="repo:"+host)`; test asserts entries exist, are advisory, and are repo-tagged.
- [ ] **Step 3: Demo bake** — Dockerfile.brain: `COPY CORRAL.md docs/corral/ /root/.claude/projects/default/memory/` (flatten; keep existing seed-memory COPY); rebuild demo brain and assert boot log `memory: indexed 7 entries` (1 existing + 6 new).
- [ ] **Step 4: CONTRIBUTING paragraph.**
- [ ] **Step 5: Green + commit**

```bash
git add CORRAL.md docs/corral/ CONTRIBUTING.md deploy/demo/Dockerfile.brain internal/ cmd/ && git commit -m "feat(seeds): CORRAL.md convention + corralai's own developer-doc seed corpus

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: Demo arc verification + docs

**Files:**
- Modify: `deploy/demo/README.md` (a "watch it learn" section), `README.md` (learning-loop bullet in What-it-does), `docs/DESIGN.md` (roadmap P8 entry)

**Steps:**
- [ ] **Step 1: Live two-run arc** — clean `make demo-mission` (stash `deploy/demo/.env` first — memory `corralai-demo-dev-env`); wait for the go-mod-init recurrence → proposal → Shep announces → UI card → Approve → `corral-admin mission create` a second directive → assert the new mission's builder instruction carries the guidance (via `list_tasks`) → confirm no recurrence of the signature in run 2. Screenshot the proposals card. Tear down, restore `.env`.
- [ ] **Step 2: Docs** — demo README section with the arc + the developer-queries story (`.mcp.json` → ask about the codebase); DESIGN.md P8 roadmap entry with what was verified; and a root-README subsection that pitches the pattern itself:

**"The knowledge corpus (CORRAL.md)"** — presented as a team development design
pattern, not just a feature: one markdown corpus that (1) developers read as
onboarding docs, (2) any developer's agent queries conversationally via the
brain (`search_memory`), (3) the herd searches before it works and extends as it
learns, and (4) grows through ordinary PRs — code review is the trust gate for
knowledge exactly as it is for code. Close with the loop: swarm-proposed,
human-approved skills feed the same corpus. Two short paragraphs + the file
convention (`CORRAL.md` root entry point, `docs/corral/*.md` corpus).
- [ ] **Step 3: Full suite + security gate** — `go test ./... -count=1 && bash scripts/check-security.sh` → both green.
- [ ] **Step 4: Commit + push + CI**

```bash
git add -A && git commit -m "feat(learn): the learning loop, demo-visible — docs + verified two-run arc

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
git push origin main && gh run watch $(gh run list -R pdbethke/corralai --limit 1 --json databaseId --jq '.[0].databaseId') -R pdbethke/corralai --exit-status
```

---

## Self-review notes (performed at write time)

- **Spec coverage:** detect (T2/T4), draft (T3), gate (T5/T8/T9), refine (T1/T7), pre-seed + community corpus (T10 + CONTRIBUTING), demo arc (T11), injection (T6), telemetry events (T4/T5/T7). Deferred items match the spec's deferred list. The spec's "seeded role authority" demo mechanism is intentionally amended: dev-mode isAdmin (identity.go:195-196) already makes the local operator the admin, and seeding Principals would lock the unauthenticated demo out — the human gate in the demo is the local operator, per Task 6 Step 2's comment requirement. The spec should be read with this amendment; noted here rather than silently diverging.
- **Type consistency:** `learn.Asker` matches `llm.Client.Ask(ctx, system, user)` (verified at `internal/llm/client.go:72`); `memory.Add` and `artifacts.Put` signatures copied from source.
- **Placeholders:** Task 5 Step 1 shows the test as a commented skeleton with exact assertions enumerated — the implementer writes it against the named harness files; all other code steps are complete.
