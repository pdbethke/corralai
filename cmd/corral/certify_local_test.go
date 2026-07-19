// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/certverify"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/transparency"
)

// blockingChatter tracks how many Chat calls overlap in time, so a test can
// prove the worker pool actually runs tasks in parallel (max > 1) and never
// exceeds the swarm bound (max <= swarm). It holds each call briefly to force
// observable overlap.
type blockingChatter struct {
	mu              *sync.Mutex
	cur, max, total *int
}

func (c blockingChatter) Chat(_ []agentworker.Message, _ []any) (agentworker.Message, error) {
	c.mu.Lock()
	*c.cur++
	*c.total++
	if *c.cur > *c.max {
		*c.max = *c.cur
	}
	c.mu.Unlock()
	time.Sleep(25 * time.Millisecond)
	c.mu.Lock()
	*c.cur--
	c.mu.Unlock()
	return agentworker.Message{Role: "assistant", Content: "ok"}, nil
}

// TestRunReadyTasks_ConcurrentAndBounded proves slice 1 of the swarm: the drive
// loop drains independent tasks through a POOL of concurrent workers (genuine
// parallelism), CAPPED at the swarm bound, and runs every task exactly once —
// jail-free (the pool needs only the queue + a fake role model, no sandbox).
func TestRunReadyTasks_ConcurrentAndBounded(t *testing.T) {
	q, err := queue.Open(filepath.Join(t.TempDir(), "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	const mission int64 = 1
	const nTasks = 6
	const swarm = 3
	var specs []queue.TaskSpec
	for i := 0; i < nTasks; i++ {
		// role mutant-generator = the structured fast path (one Chat call, no tool loop)
		specs = append(specs, queue.TaskSpec{Key: fmt.Sprintf("t%d", i), Role: "mutant-generator", Title: "t", Instruction: "do"})
	}
	if err := q.Enqueue(mission, specs); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var cur, max, total int
	chatterFor := func(string) agentworker.Chatter {
		return blockingChatter{mu: &mu, cur: &cur, max: &max, total: &total}
	}

	ran, err := runReadyTasks(context.Background(), q, mission, chatterFor, nil, nil, swarm)
	if err != nil {
		t.Fatalf("runReadyTasks: %v", err)
	}
	if !ran {
		t.Fatal("expected ran=true")
	}
	if total != nTasks {
		t.Fatalf("every task must run exactly once: total=%d, want %d", total, nTasks)
	}
	if max <= 1 {
		t.Fatalf("no parallelism observed (max concurrent = %d) — the pool must run independent tasks together", max)
	}
	if max > swarm {
		t.Fatalf("swarm bound violated: max concurrent %d > swarm %d", max, swarm)
	}
	if leftover, _ := q.ClaimNext("check", nil, 1); leftover != nil {
		t.Fatalf("all tasks must be drained; still claimable: %+v", leftover)
	}
}

// TestLoadRepoFiles_SkipsGitBinaryAndKeysRepoRelative proves --repo-dir's repo
// loader: it keys files by slash-separated repo-relative path, skips the .git
// dir and non-UTF-8 (binary) files, and reads real source through.
func TestLoadRepoFiles_SkipsGitBinaryAndKeysRepoRelative(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"more_itertools", "tests", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string, b []byte) {
		if err := os.WriteFile(filepath.Join(root, rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("more_itertools/recipes.py", []byte("def f():\n    return 1\n"))
	write("tests/test_recipes.py", []byte("import more_itertools\n"))
	write(".git/config", []byte("[core]\n"))
	write("logo.bin", []byte{0xff, 0xfe, 0x00, 0x01}) // invalid UTF-8 -> skipped

	files, err := loadRepoFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["more_itertools/recipes.py"]; !ok {
		t.Fatalf("expected more_itertools/recipes.py in the seed; got %v", keysOf(files))
	}
	if _, ok := files["tests/test_recipes.py"]; !ok {
		t.Fatalf("expected tests/test_recipes.py in the seed; got %v", keysOf(files))
	}
	for k := range files {
		if strings.HasPrefix(k, ".git/") || k == ".git" {
			t.Fatalf(".git must be skipped, found %q", k)
		}
		if k == "logo.bin" {
			t.Fatal("binary (non-UTF-8) files must be skipped")
		}
	}
}

func keysOf(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestRecordSink_TapeShape proves the --record collector produces the exact
// {events:[{ts,kind,actor,subject,detail}]} tape the cockpit replays: beats are
// ts-ordered from 1, the driver's EventSink beats are attributed to the pool,
// and drive-loop beats carry their actor.
func TestRecordSink_TapeShape(t *testing.T) {
	r := &recordSink{}
	r.add("task_created", "", "mutant-generator", map[string]any{"role": "mutant-generator"})
	r.add("task_claimed", "claude-sonnet-5/mutant-generator", "mutant-generator", nil)
	r.Emit(0, "pool_dev_adequacy", "", map[string]any{"dev_kill_rate": 1.0}) // EventSink path
	r.add("task_done", "claude-sonnet-5/mutant-generator", "mutant-generator", nil)

	if len(r.events) != 4 {
		t.Fatalf("want 4 beats, got %d", len(r.events))
	}
	for i, e := range r.events {
		if e.Ts != i+1 {
			t.Errorf("beat %d ts = %d, want %d (monotonic from 1)", i, e.Ts, i+1)
		}
	}
	if r.events[2].Actor != "corral-advpool" {
		t.Errorf("EventSink beat must be attributed to the pool, got %q", r.events[2].Actor)
	}
	if r.events[1].Actor != "claude-sonnet-5/mutant-generator" {
		t.Errorf("drive-loop beat lost its actor: %q", r.events[1].Actor)
	}

	// The tape round-trips to the cockpit's {events:[…]} JSON.
	path := filepath.Join(t.TempDir(), "tape.json")
	if err := r.writeTape(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tape struct {
		Events []recEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &tape); err != nil {
		t.Fatalf("tape is not valid {events} JSON: %v", err)
	}
	if len(tape.Events) != 4 || tape.Events[0].Kind != "task_created" {
		t.Fatalf("tape did not round-trip: %+v", tape.Events)
	}
}

// nil recordSink must be a total no-op (the non-recording path).
func TestRecordSink_NilIsNoop(t *testing.T) {
	var r *recordSink
	r.add("x", "a", "s", nil) // must not panic
}

// TestWriteLocalRecordFile_RoundTripsThroughVerify proves the `--out` file a
// --local run writes re-verifies through the EXACT offline path `corral certify
// verify` uses: sign a verdict into the ledger (as the driver does), export it
// with writeLocalRecordFile, then unmarshal into certRecord and run
// certverify.VerifyRecord under the out-of-band public key. A --local record is
// signed-but-not-witnessed, so it verifies only with allowUnanchored=true —
// which is exactly what the printed verify hint tells the user to pass.
func TestWriteLocalRecordFile_RoundTripsThroughVerify(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Sign a synthetic verdict into the ledger exactly as the --local driver's
	// CertSigner does, then carry its id/head onto the Verdict the CLI holds.
	s := advpool.CertSigner{Key: priv, Store: bs}
	v := advpool.Verdict{
		Repo: "local", Commit: "local", Status: advpool.StatusCertified,
		DevKillRate: 0.2,
		ModelsByRole: advpool.RoleAssignment{
			advpool.RoleMutantGenerator: "claude-sonnet-5",
			advpool.RoleTestWriter:      "claude-sonnet-5",
			advpool.RoleTestCritic:      "claude-haiku-4-5",
		},
	}
	id, head, err := s.SignVerdict(context.Background(), v)
	if err != nil {
		t.Fatalf("SignVerdict: %v", err)
	}
	v.RecordID, v.RecordHead = id, head

	out := filepath.Join(dir, "verdict.json")
	if err := writeLocalRecordFile(out, bs, priv, v); err != nil {
		t.Fatalf("writeLocalRecordFile: %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var cr certRecord
	if err := json.Unmarshal(raw, &cr); err != nil {
		t.Fatalf("--out file is not a parseable record: %v", err)
	}
	if cr.PublicKey != hex.EncodeToString(pub) {
		t.Fatalf("exported public_key = %q, want the signer's key %q", cr.PublicKey, hex.EncodeToString(pub))
	}
	crec := certverify.Record{
		Statement: cr.Statement, Signature: cr.Signature, Steps: cr.Steps,
		Head: cr.Head, Rekor: cr.Rekor, Anchored: cr.Anchored,
	}
	checks, allOK := certverify.VerifyRecord(crec, pub, func() (transparency.Witness, error) {
		return transparency.NewFakeWitness(), nil
	}, true)
	if !allOK {
		for _, c := range checks {
			if !c.OK {
				t.Fatalf("offline verify of the --out file failed at check %q: %s", c.Name, c.Detail)
			}
		}
		t.Fatal("certverify.VerifyRecord: allOK=false with no failing check named")
	}
}

// cannedChatter is a fake agentworker.Chatter that always replies with the same
// content and never a tool call — enough to stand in for a role's model:
// mutant-generator/test-writer read the content verbatim (structured fast
// path), and the test-critic sees a plain reply with no report_finding call
// (so it files no findings).
type cannedChatter struct{ content string }

func (c cannedChatter) Chat(_ []agentworker.Message, _ []any) (agentworker.Message, error) {
	return agentworker.Message{Role: "assistant", Content: c.content}, nil
}

// sequenceChatter replies with successive entries from replies on each call
// (sticking on the last entry once exhausted) — used to make a role's FIRST
// artifact a recoverable dud (e.g. non-compiling test-writer output) and its
// RETRY the real thing, so a test can exercise the driver's
// reopen-then-reissue path and driveLocalRun's tolerance of the resulting
// Tick error.
type sequenceChatter struct {
	mu      sync.Mutex
	replies []string
	calls   int
}

func (c *sequenceChatter) Chat(_ []agentworker.Message, _ []any) (agentworker.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	if i >= len(c.replies) {
		i = len(c.replies) - 1
	}
	c.calls++
	return agentworker.Message{Role: "assistant", Content: c.replies[i]}, nil
}

// The hermetic Go target: a password validator whose goal is "≥ 12 chars", the
// dev's own (partial) test for it, three mutants, and a pool-authored test that
// kills the one mutant the dev suite misses.
const localTargetCode = `package control

import "errors"

// ValidatePassword returns an error when pw is shorter than 12 characters.
func ValidatePassword(pw string) error {
	if len(pw) < 12 {
		return errors.New("password too short")
	}
	return nil
}
`

// The dev suite kills m1 (accepts all) and m2 (len<4) but MISSES m3 (len<11):
// its 8-char probe can't distinguish the 11-char boundary.
const localDevTest = `package control

import "testing"

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("abcd1234"); err == nil {
		t.Fatal("expected an error for an 8-char password")
	}
	if err := ValidatePassword("aVeryLongPassword123"); err != nil {
		t.Fatalf("unexpected error for a long password: %v", err)
	}
}
`

// Three seeded-violation mutants in testgen's parseable format. Each is a
// complete, compiling drop-in for validate.go that no longer satisfies the goal.
const localMutants = "===MUTATION_1===\n" +
	"package control\n\n" +
	"func ValidatePassword(pw string) error {\n\treturn nil\n}\n" +
	"===MUTATION_2===\n" +
	"package control\n\n" +
	"import \"errors\"\n\n" +
	"func ValidatePassword(pw string) error {\n\tif len(pw) < 4 {\n\t\treturn errors.New(\"password too short\")\n\t}\n\treturn nil\n}\n" +
	"===MUTATION_3===\n" +
	"package control\n\n" +
	"import \"errors\"\n\n" +
	"func ValidatePassword(pw string) error {\n\tif len(pw) < 11 {\n\t\treturn errors.New(\"password too short\")\n\t}\n\treturn nil\n}\n"

// The pool's authored test kills the survivor m3: an 11-char password must be
// rejected, which the compliant code does (11 < 12) but m3 (len<11) does not.
const localWriterTest = `package control

import "testing"

func TestPoolElevenCharsRejected(t *testing.T) {
	if err := ValidatePassword("elevenchars"); err == nil {
		t.Fatal("expected an error for an 11-char password")
	}
}
`

// TestDriveLocalRun_EndToEnd drives the FULL --local orchestration seam
// (driveLocalRun) over the REAL jail-backed Scorer/Validator and the REAL
// certify-chain Signer, with only the LLM faked: canned mutants a canned dev
// suite partly kills, a canned pool test that kills the survivor, and a canned
// critic that files nothing. It asserts the run converges to a signed verdict
// whose record independently verifies with certify.VerifyDSSE — the same
// sign/verify path `corral certify verify` runs.
//
// Skips cleanly when no sandbox jail is available (e.g. bwrap's unprivileged
// userns blocked on this host); the audit refuses to run tests unsandboxed, so
// there is nothing to exercise without a jail. It runs for real in CI / any
// host with a working jail.
func TestDriveLocalRun_EndToEnd(t *testing.T) {
	// Default to the OS's auto backend (bwrap on Linux). $CORRAL_TEST_JAIL lets a
	// dev/CI force a specific backend — e.g. CORRAL_TEST_JAIL=none with
	// AGENT_EXEC_UNSAFE_HOST=1 to exercise the real drive loop on a disposable
	// host whose `go` is unreachable inside bwrap (snap-packaged outside /usr).
	iso, err := sandbox.Resolve(sandbox.Config{
		Backend:    os.Getenv("CORRAL_TEST_JAIL"),
		UnsafeHost: os.Getenv("AGENT_EXEC_UNSAFE_HOST") == "1",
	})
	if err != nil || iso == nil {
		t.Skipf("no working sandbox jail on this host (%v) — --local refuses to run tests unsandboxed; skipping", err)
	}
	jail := adequacy.NewJail(iso, 120*time.Second)

	// A resolvable bwrap backend is not enough: bwrap only binds /usr into the
	// sandbox, so a host with a snap-packaged `go` (outside /usr, common on
	// Ubuntu dev boxes) has a working jail that still can't run `go` — the
	// scorer's compliant-code run would then "fail" for a reason unrelated to
	// the audit. Smoke-test the toolchain inside the jail and skip cleanly if
	// it can't run, mirroring internal/advpool's advPoolJailSkipUnlessGoWorks.
	if pass, rerr := jail.RunTest(context.Background(), nil, []string{"go", "version"}); rerr != nil || !pass {
		t.Skipf("go toolchain not reachable inside the jail on this host (rerr=%v pass=%v) — likely a snap-packaged go outside /usr; skipping", rerr, pass)
	}

	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "queue.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })

	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "model-gen",
		advpool.RoleTestWriter:      "model-writer",
		advpool.RoleTestCritic:      "model-critic",
	}
	d, err := advpool.NewDriver(q, advpool.JailScorer{Jail: jail}, advpool.JailValidator{Jail: jail}, assign, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	d.Signer = advpool.CertSigner{Key: priv, Store: bs, Witness: nil}

	rs := advpool.RunSpec{
		Repo: "local", Commit: "local",
		Goal:        "passwords must be at least 12 characters",
		CodePath:    "validate.go",
		Code:        localTargetCode,
		DevTestPath: "validate_test.go",
		DevTestCode: localDevTest,
		NMutants:    3,
		Lang:        "go",
	}
	const missionID = 7
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatal(err)
	}

	chatterFor := func(role string) agentworker.Chatter {
		switch role {
		case advpool.RoleMutantGenerator:
			return cannedChatter{content: localMutants}
		case advpool.RoleTestWriter:
			return cannedChatter{content: localWriterTest}
		case advpool.RoleTestCritic:
			return cannedChatter{content: "the dev tests exercise the boundary; no vacuous tests found"}
		default:
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tape := &recordSink{}
	actorFor := func(role string) string { return recordActor(role, assign[role]) }
	verdict, err := driveLocalRun(ctx, d, q, missionID, chatterFor, time.Millisecond, func(time.Duration) {}, io.Discard, tape, actorFor, 2)
	if err != nil {
		t.Fatalf("driveLocalRun: %v", err)
	}
	if verdict == nil {
		t.Fatal("expected a converged verdict, got nil")
	}
	// The --record tape captured the run: the task lifecycle from the drive loop
	// plus the pool's reasoning beats from the driver's EventSink.
	kinds := map[string]int{}
	for _, e := range tape.events {
		kinds[e.Kind]++
	}
	for _, want := range []string{"task_created", "task_claimed", "task_done", "pool_subject", "pool_verdict"} {
		if kinds[want] == 0 {
			t.Errorf("tape missing a %q beat; got kinds %v", want, kinds)
		}
	}

	// The dev suite killed 2 of 3 mutants (0.667 ≥ 0.5 threshold) and the pool's
	// authored test proved the one survivor real — a certified, signed verdict.
	if verdict.Status != advpool.StatusCertified {
		t.Fatalf("Status = %q, want %q (dev kill-rate %.3f)", verdict.Status, advpool.StatusCertified, verdict.DevKillRate)
	}
	if verdict.MutantsTotal != 3 {
		t.Fatalf("MutantsTotal = %d, want 3", verdict.MutantsTotal)
	}
	if verdict.Survivors != 1 {
		t.Fatalf("Survivors = %d, want 1 (m3, the 11-char boundary)", verdict.Survivors)
	}
	if verdict.ProvenMissed != 1 {
		t.Fatalf("ProvenMissed = %d, want 1 (the pool's test kills m3)", verdict.ProvenMissed)
	}
	if verdict.RecordID <= 0 {
		t.Fatalf("RecordID = %d, want a signed record id > 0", verdict.RecordID)
	}

	// The signed record must independently verify — the same DSSE check
	// `corral certify verify` runs over the user's own public key.
	rec, found, err := bs.Get(verdict.RecordID)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", verdict.RecordID, found, err)
	}
	sig, ok := rec["signature"].(string)
	if !ok || sig == "" {
		t.Fatalf("stored record %d missing signature", verdict.RecordID)
	}
	stmt, ok, verr := certify.VerifyDSSE([]byte(sig), pub)
	if verr != nil {
		t.Fatalf("VerifyDSSE: %v", verr)
	}
	if !ok || stmt == nil {
		t.Fatal("VerifyDSSE must succeed over the signed --local verdict record under the run's public key")
	}
}

// A non-compiling first draft of the pool's test: syntactically broken (a
// missing closing brace), so JailValidator.CompileTest rejects it and the
// driver reopens the test-writer task — the RECOVERABLE condition this fix is
// about (a frontier model's common non-compiling first attempt).
const localWriterTestBroken = `package control

import "testing"

func TestPoolElevenCharsRejected(t *testing.T) {
	if err := ValidatePassword("elevenchars"); err == nil {
		t.Fatal("expected an error for an 11-char password")
	}
`

// TestDriveLocalRun_TolerateOneRecoverableTickError proves the bug-fix: a
// single non-compiling test-writer artifact — which makes the driver reopen
// the task and Tick return an error ("reissued for retry") — must NOT abort
// driveLocalRun. The retry (this test's sequenceChatter's second reply) is a
// valid, compiling test, and the run must still converge to a signed
// certified verdict, proving the reopened task really was re-claimed and
// re-run, not just logged and dropped.
//
// Uses the same real-jail-or-skip pattern as TestDriveLocalRun_EndToEnd: the
// tolerance only has something to exercise when JailValidator.CompileTest is
// the REAL compiler rejecting the REAL broken source.
func TestDriveLocalRun_TolerateOneRecoverableTickError(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{
		Backend:    os.Getenv("CORRAL_TEST_JAIL"),
		UnsafeHost: os.Getenv("AGENT_EXEC_UNSAFE_HOST") == "1",
	})
	if err != nil || iso == nil {
		t.Skipf("no working sandbox jail on this host (%v) — --local refuses to run tests unsandboxed; skipping", err)
	}
	jail := adequacy.NewJail(iso, 120*time.Second)
	if pass, rerr := jail.RunTest(context.Background(), nil, []string{"go", "version"}); rerr != nil || !pass {
		t.Skipf("go toolchain not reachable inside the jail on this host (rerr=%v pass=%v) — likely a snap-packaged go outside /usr; skipping", rerr, pass)
	}

	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "queue.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })

	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "model-gen",
		advpool.RoleTestWriter:      "model-writer",
		advpool.RoleTestCritic:      "model-critic",
	}
	d, err := advpool.NewDriver(q, advpool.JailScorer{Jail: jail}, advpool.JailValidator{Jail: jail}, assign, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	d.Signer = advpool.CertSigner{Key: priv, Store: bs, Witness: nil}

	rs := advpool.RunSpec{
		Repo: "local", Commit: "local",
		Goal:        "passwords must be at least 12 characters",
		CodePath:    "validate.go",
		Code:        localTargetCode,
		DevTestPath: "validate_test.go",
		DevTestCode: localDevTest,
		NMutants:    3,
		Lang:        "go",
	}
	const missionID = 8
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatal(err)
	}

	writerChatter := &sequenceChatter{replies: []string{localWriterTestBroken, localWriterTest}}
	chatterFor := func(role string) agentworker.Chatter {
		switch role {
		case advpool.RoleMutantGenerator:
			return cannedChatter{content: localMutants}
		case advpool.RoleTestWriter:
			return writerChatter
		case advpool.RoleTestCritic:
			return cannedChatter{content: "the dev tests exercise the boundary; no vacuous tests found"}
		default:
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var progress bytes.Buffer
	verdict, err := driveLocalRun(ctx, d, q, missionID, chatterFor, time.Millisecond, func(time.Duration) {}, &progress, nil, nil, 1)
	if err != nil {
		t.Fatalf("driveLocalRun: %v (progress log:\n%s)", err, progress.String())
	}
	if verdict == nil {
		t.Fatal("expected a converged verdict, got nil")
	}
	if verdict.Status != advpool.StatusCertified {
		t.Fatalf("Status = %q, want %q (one bad test-writer artifact must not abort the run)", verdict.Status, advpool.StatusCertified)
	}
	if writerChatter.calls < 2 {
		t.Fatalf("writerChatter.calls = %d, want >= 2 (the reopened task must be re-claimed and re-run)", writerChatter.calls)
	}
	if !strings.Contains(progress.String(), "reissued for retry") {
		t.Fatalf("expected the drive loop to log a reissued-for-retry line, got:\n%s", progress.String())
	}

	// The recovered run must still sign+verify like any other converged run.
	rec, found, err := bs.Get(verdict.RecordID)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", verdict.RecordID, found, err)
	}
	sig, ok := rec["signature"].(string)
	if !ok || sig == "" {
		t.Fatalf("stored record %d missing signature", verdict.RecordID)
	}
	if _, ok, verr := certify.VerifyDSSE([]byte(sig), pub); verr != nil || !ok {
		t.Fatalf("VerifyDSSE: ok=%v err=%v", ok, verr)
	}
}
