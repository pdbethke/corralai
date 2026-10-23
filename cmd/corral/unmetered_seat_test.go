// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
	"github.com/pdbethke/corralai/internal/statedir"
)

// wiredAgent defines the operator agent NAME on the given wire ("" = text)
// as a script that logs every non-version call to the returned file and
// prints reply. No real CLI runs.
func wiredAgent(t *testing.T, name, wire, reply string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	path := filepath.Join(dir, "agent-"+name)
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'fakeagent 9.9'; exit 0; fi\ncat >/dev/null\necho call >> " + log + "\nprintf '%s' '" + reply + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	key := "CORRALAI_AGENT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	t.Setenv(key, path)
	t.Setenv(key+"_WIRE", wire)
	return log
}

const claudeJSONReply = `{"type":"result","is_error":false,"terminal_reason":"completed","result":"ok","usage":{"input_tokens":7,"output_tokens":3}}`

// --- costLine -----------------------------------------------------------

// A role whose every call was unmetered never prints a token count.
func TestCostLineAllUnmeteredRoleSaysNotMetered(t *testing.T) {
	got := costLine([]advpool.ModelCall{
		{Role: advpool.RoleTestWriter, Model: "fakea", Calls: 3, UnmeteredCalls: 3},
	})
	want := "  cost: 3 calls not metered — test-writer not metered (3 calls)"
	if got != want {
		t.Errorf("costLine =\n%q\nwant\n%q", got, want)
	}
}

// A mixed role prints the tokens it has and how many calls are not in them;
// the total line adds the unmetered calls rather than folding them in.
func TestCostLineMixedRoleCountsWhatIsNotInItsTokens(t *testing.T) {
	got := costLine([]advpool.ModelCall{
		{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 3, InputTokens: 100, OutputTokens: 50},
		{Role: advpool.RoleTestWriter, Model: "fakea", Calls: 4, UnmeteredCalls: 1, InputTokens: 1200, OutputTokens: 300},
		{Role: advpool.RoleTestCritic, Model: "fakec", Calls: 1, UnmeteredCalls: 1},
	})
	want := "  cost: 1.3k tokens in / 350 out across 6 calls + 2 calls not metered — mutant-generator 100/50 (3 calls), test-writer 1.2k/300 (4 calls, 1 not metered), test-critic not metered (1 call)"
	if got != want {
		t.Errorf("costLine =\n%q\nwant\n%q", got, want)
	}
}

// With nothing unmetered the line is exactly what it always was.
func TestCostLineWithNoUnmeteredCallsIsUnchanged(t *testing.T) {
	got := costLine([]advpool.ModelCall{
		{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 3, InputTokens: 100, OutputTokens: 50},
	})
	if want := "  cost: 100 tokens in / 50 out across 3 calls — mutant-generator 100/50 (3 calls)"; got != want {
		t.Errorf("costLine =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "not metered") {
		t.Errorf("a fully metered line mentions metering: %q", got)
	}
}

// --- the cap refusal ----------------------------------------------------

const capBlindWant = "--max-tokens: seat test-writer (fakew) reports no token usage, so the cap cannot see it — drop the cap, or use an agent whose wire reports usage (CORRALAI_AGENT_FAKEW_WIRE)"

// A cap that cannot see a seat is not a cap: the router refuses to build.
func TestATokenCapRefusesAnUnmeteredSeat(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", "x")
	wiredAgent(t, "fakem", "claude-json", claudeJSONReply)
	assign := advpool.RoleAssignment{advpool.RoleMutantGenerator: "fakem", advpool.RoleTestWriter: "fakew"}
	_, err := localChatterFor(assign, nil, nil, agentbackend.NewTokenBudget(1000))
	if err == nil || err.Error() != capBlindWant {
		t.Fatalf("got %v\nwant %s", err, capBlindWant)
	}
	// No cap, no refusal: the seat runs and says it is not metered.
	if _, err := localChatterFor(assign, nil, nil, nil); err != nil {
		t.Fatalf("with no cap an unmetered seat must run: %v", err)
	}
	// A cap with every seat metered is no refusal either.
	t.Setenv("CORRALAI_AGENT_FAKEW_WIRE", "claude-json")
	if _, err := localChatterFor(assign, nil, nil, agentbackend.NewTokenBudget(1000)); err != nil {
		t.Fatalf("a cap over metered seats must be accepted: %v", err)
	}
}

// certify --local refuses before any jail, store or spend: exit 2, the seat
// named, no agent call made and no build ledger created.
func TestCertifyLocalMaxTokensRefusesAnUnmeteredSeatBeforeAnything(t *testing.T) {
	noKeys(t)
	t.Setenv("CORRALAI_STATE_DIR", t.TempDir())
	wlog := wiredAgent(t, "fakew", "", "x")
	mlog := wiredAgent(t, "fakem", "claude-json", claudeJSONReply)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(dir, "a_test.go"), "package a\n")
	mustWrite(t, filepath.Join(dir, "go.mod"), "module a\n\ngo 1.22\n")
	var out, errb bytes.Buffer
	code := runCertifyLocal([]string{
		"--repo-dir", dir, "--code", "a.go", "--goal", "returns one",
		"--mutant-model", "fakem", "--writer-model", "fakew", "--critic-model", "off",
		"--max-tokens", "1000", "--", "go", "test", "./...",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2\nstdout:%s\nstderr:%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), capBlindWant) {
		t.Fatalf("stderr does not carry the refusal:\n%s", errb.String())
	}
	for _, l := range []string{wlog, mlog} {
		if _, err := os.Stat(l); err == nil {
			t.Errorf("an agent was called before the refusal (%s exists)", l)
		}
	}
	p, err := statedir.Path(statedir.BuildDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Errorf("the build ledger %s was created before the refusal", p)
	}
}

// certify --repo refuses the same way, before goal derivation spends.
func TestCertifyRepoMaxTokensRefusesAnUnmeteredSeatBeforeDerivation(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", "x")
	wiredAgent(t, "fakem", "claude-json", claudeJSONReply)
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	derived := 0
	prev := certifyRepoDeriver
	t.Cleanup(func() { certifyRepoDeriver = prev })
	certifyRepoDeriver = func(model, _ string) (reposcan.Deriver, error) {
		derived++
		return newMeteredDeriver(&fakeBackend{reply: "NONE"}, model), nil
	}
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--derive-model", "fakem", "--no-goal-cache", "--ledger", ledgerDir,
		"--writer-model", "fakew", "--mutant-model", "fakem", "--critic-model", "off",
		"--substrate", substrateWorkspace, "--max-tokens", "1000",
		"--", "true",
	}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), capBlindWant) {
		t.Fatalf("exit %d, want 2 with the refusal\nstderr:%s", code, errb.String())
	}
	if derived != 0 {
		t.Errorf("a goal deriver was built before the refusal")
	}
	if entries, _ := filepath.Glob(filepath.Join(ledgerDir, "scans", "*")); len(entries) != 0 {
		t.Errorf("a ledger entry was written: %v", entries)
	}
}

// --- the value, followed to the surface ---------------------------------

// ONE test that follows UnmeteredCalls from an agent seat's reply through
// every field-by-field converter: meter -> verdict ModelCall -> the verdict
// cache's JSON -> the scan's ledger rows and its cost line -> the signed
// ledger entry -> the ledger reader -> `scans show`'s cost line and JSON ->
// the warehouse row, whose token columns are NULL, not 0.
func TestUnmeteredCallsSurviveFromTheSeatToEverySurface(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", "plain reply")
	assign := advpool.RoleAssignment{advpool.RoleTestWriter: "fakew"}
	meters := auditRoleMeters(assign)
	chatterFor, err := localChatterFor(assign, meters, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := chatterFor(advpool.RoleTestWriter).Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	calls := modelCallsFromMeters(meters)
	if len(calls) != 1 || calls[0].Calls != 2 || calls[0].UnmeteredCalls != 2 {
		t.Fatalf("meter -> ModelCall: %+v", calls)
	}

	// The verdict cache stores the verdict as JSON and reads it back.
	js, err := marshalVerdict(advpool.Verdict{ModelCalls: calls})
	if err != nil {
		t.Fatal(err)
	}
	var cached advpool.Verdict
	if err := json.Unmarshal([]byte(js), &cached); err != nil {
		t.Fatal(err)
	}
	if cached.ModelCalls[0].UnmeteredCalls != 2 {
		t.Fatalf("verdict JSON dropped it: %+v (%s)", cached.ModelCalls, js)
	}

	results := []reposcan.FileResult{{Job: reposcan.Job{Path: "pkg/a.go", Lang: "go"}, Gradable: true, Verdict: cached}}
	if line := costLine(scanModelCallTotals(results)); !strings.Contains(line, "test-writer not metered (2 calls)") {
		t.Fatalf("the scan's cost line: %q", line)
	}
	rows := buildScanModelCallRows(results)
	if len(rows) != 1 || rows[0].UnmeteredCalls != 2 {
		t.Fatalf("verdict -> ledger row: %+v", rows)
	}

	scan := scanstore.Scan{Owner: "o", Repo: "r", Commit: "deadbeef"}
	files := []scanstore.File{{Path: "pkg/a.go", Lang: "go", Disposition: "audited", TotalMillis: int64Ptr(5)}}
	b := buildBundle(scan, 0, files, nil, rows, nil, auditpush.Link{}, false, "o/r", "deadbeef", "", bundleMeta{})
	if len(b.Calls) != 1 || b.Calls[0].UnmeteredCalls != 2 {
		t.Fatalf("ledger row -> bundle row: %+v", b.Calls)
	}

	// The signed ledger entry, and the reader `scans show` uses.
	dir := filepath.Join(t.TempDir(), "ledger")
	if _, err := pushBundle(dir+"/", b, io.Discard); err != nil {
		t.Fatal(err)
	}
	st, err := openLedgerScans(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	back, err := st.ModelCallsForScan(context.Background(), 1)
	if err != nil || len(back) != 1 || back[0].UnmeteredCalls != 2 {
		t.Fatalf("ledger entry -> reader: %+v %v", back, err)
	}
	if line := costLine(modelCallsByPath(back)["pkg/a.go"]); !strings.Contains(line, "test-writer not metered (2 calls)") {
		t.Fatalf("scans show's cost line: %q", line)
	}
	var js2 bytes.Buffer
	runScansShowJSONWithTiming(st, 1, nil, &js2, &bytes.Buffer{})
	var shown struct {
		ModelCalls []map[string]any `json:"model_calls"`
	}
	if err := json.Unmarshal(js2.Bytes(), &shown); err != nil || len(shown.ModelCalls) != 1 {
		t.Fatalf("scans show --json: %v %s", err, js2.String())
	}
	if mc := shown.ModelCalls[0]; mc["unmetered_calls"] != float64(2) || mc["input_tokens"] != nil || mc["output_tokens"] != nil {
		t.Fatalf("scans show --json must say 2 unmetered calls and null tokens: %v", mc)
	}

	// The warehouse.
	wh := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := pushBundle(wh, b, io.Discard); err != nil {
		t.Fatal(err)
	}
	db, err := attachWarehouse(wh, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var unmetered int
	var in, outTok *int64
	if err := db.QueryRow(`SELECT unmetered_calls, input_tokens, output_tokens FROM corral_model_calls`).Scan(&unmetered, &in, &outTok); err != nil {
		t.Fatal(err)
	}
	if unmetered != 2 || in != nil || outTok != nil {
		t.Fatalf("warehouse row: unmetered=%d input=%v output=%v — want 2 and NULL tokens", unmetered, in, outTok)
	}
	rb, err := auditpush.ReadBundle(db, "o/r", 0)
	if err != nil || len(rb.Calls) != 1 || rb.Calls[0].UnmeteredCalls != 2 {
		t.Fatalf("warehouse -> ReadBundle: %+v %v", rb.Calls, err)
	}
}

// The goal-deriver records its own rows; an unmetered derive seat's calls
// must reach them and the scan's cost line the same way.
func TestAnUnmeteredDeriveSeatReachesItsRowsAndTheCostLine(t *testing.T) {
	d := newMeteredDeriver(&fakeBackend{reply: "a goal", usage: agentbackend.Usage{Unmetered: true}}, "fakem")
	if _, _, err := d.Derive(context.Background(), reposcan.Candidate{Path: "pkg/a.go", Lang: "go"}, "package pkg\n"); err != nil {
		t.Fatal(err)
	}
	rows := d.(usageReporter).modelCallRows()
	if len(rows) != 1 || rows[0].UnmeteredCalls != 1 || rows[0].Calls != 1 {
		t.Fatalf("deriver rows: %+v", rows)
	}
	if line := costLine(scanModelCallTotalsWith(nil, d)); !strings.Contains(line, "goal-deriver not metered (1 call)") {
		t.Fatalf("cost line: %q", line)
	}
}

func int64Ptr(v int64) *int64 { return &v }

// Review Focus 5: review has no cap, so an unmetered seat is not refused —
// it runs, and its record says what it could not measure: the call is
// counted as unmetered, and the warehouse's token columns are NULL, never 0.
func TestReviewRunsAnUnmeteredSeatAndRecordsItNotMetered(t *testing.T) {
	requireReviewSandbox(t)
	noKeys(t)
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	wiredAgent(t, "plainrev", "", `{"opinion":"Add subtracts","findings":[{"claim":"Add subtracts","tier":"REPRODUCED","file":"pkg/a.go","line":3,"severity":"high","script":"grep -q \"a - b\" pkg/a.go"}],"sound":["go.mod","pkg","the module"]}`)

	ledger := filepath.Join(t.TempDir(), "ledger")
	wh := filepath.Join(t.TempDir(), "wh.duckdb")
	var out, errb bytes.Buffer
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "plainrev", "--ledger", ledger, "--push", wh}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil || len(entries) != 1 || entries[0].Review == nil {
		t.Fatalf("want one review entry: %v %v", entries, err)
	}
	r := entries[0].Review
	if r.Calls != 1 || r.UnmeteredCalls != 1 || r.InputTokens != 0 || r.OutputTokens != 0 {
		t.Fatalf("the record must say 1 call, 1 not metered, no tokens: calls=%d unmetered=%d tokens=%d/%d", r.Calls, r.UnmeteredCalls, r.InputTokens, r.OutputTokens)
	}
	db, err := attachWarehouse(wh, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var unmetered int
	var in, outTok *int64
	if err := db.QueryRow(`SELECT unmetered_calls, input_tokens, output_tokens FROM corral_reviews`).Scan(&unmetered, &in, &outTok); err != nil {
		t.Fatal(err)
	}
	if unmetered != 1 || in != nil || outTok != nil {
		t.Fatalf("corral_reviews: unmetered=%d input=%v output=%v — want 1 and NULL tokens", unmetered, in, outTok)
	}
}
