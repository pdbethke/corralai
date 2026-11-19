// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/models"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

// The pool every test names unless it says otherwise: the one in the spec's
// sample plan (5.3).
const samplePool = "anthropic/claude-sonnet-5-5,anthropic/claude-haiku-5-5,codex:gpt-6"

const sampleSource = "the machine ledger /state/ledger (31 scan(s), 2 review(s); a run is one SCAN)"

// poolEnv isolates the resolver from the machine: no registry, no gateway, no
// daemon, and a credential check that passes, so no test reads a real key.
// It returns the repo root to resolve against (an empty directory, so no
// checkout registry exists).
func poolEnv(t *testing.T) string {
	t.Helper()
	t.Setenv(models.EnvInline, "")
	t.Setenv(models.EnvFile, "")
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("OLLAMA_URL", "")
	orig := shadowMemberRunnable
	t.Cleanup(func() { shadowMemberRunnable = orig })
	shadowMemberRunnable = func(agentbackend.ResolvedSeat) error { return nil }
	return t.TempDir()
}

// certifyRoles are the four certify seats as a door hands them over: each
// role tied to the variable its own flag parsed into.
type certifyVals struct{ derive, mutant, writer, critic string }

func (v *certifyVals) roles() []poolRole {
	return []poolRole{
		{role: modelrank.SeatGoalDeriver, val: &v.derive},
		{role: modelrank.SeatMutantGenerator, val: &v.mutant},
		{role: modelrank.SeatTestWriter, val: &v.writer},
		{role: modelrank.SeatTestCritic, val: &v.critic},
	}
}

// genObs is one generator run per run name: 10 planted, 9 graded, 1 invalid,
// 4 survived.
func genObs(model, lang string, runs ...string) []modelrank.Observation {
	var out []modelrank.Observation
	for _, r := range runs {
		out = append(out, modelrank.Observation{Model: model, Role: modelrank.SeatMutantGenerator, Lang: lang, Run: r,
			MutantsPlanted: 10, MutantsGraded: 9, MutantsInvalid: 1, MutantsSurvived: 4})
	}
	return out
}

func writerObs(model, lang, run string, proven, attempted int) modelrank.Observation {
	return modelrank.Observation{Model: model, Role: modelrank.SeatTestWriter, Lang: lang, Run: run,
		Catches: proven, Opportunities: attempted}
}

// sampleEvidence is the spec's sample: claude-sonnet-5-5 measured over six
// python runs in both measured seats, 193/195 as writer.
func sampleEvidence() rankEvidence {
	runs := []string{"r1", "r2", "r3", "r4", "r5", "r6"}
	obs := genObs("claude-sonnet-5-5", "python", runs...)
	for _, r := range runs[:5] {
		obs = append(obs, writerObs("claude-sonnet-5-5", "python", r, 32, 32))
	}
	obs = append(obs, writerObs("claude-sonnet-5-5", "python", "r6", 33, 35))
	return rankEvidence{Obs: obs, Source: sampleSource,
		Generations: generationRange{Min: "40", Max: "43", Scans: 6}}
}

// fakeConfirm records whether it was asked, and with what plan.
type fakeConfirm struct {
	accepted, interactive bool
	err                   error
	calls                 int
	plan                  string
}

func (c *fakeConfirm) fn(plan string) (bool, bool, error) {
	c.calls++
	c.plan = plan
	return c.accepted, c.interactive, c.err
}

// fakeLoad returns ev (or err) and records every source it was asked for.
type fakeLoad struct {
	ev    rankEvidence
	err   error
	asked []rankSource
}

func (l *fakeLoad) fn(src rankSource) (rankEvidence, error) {
	l.asked = append(l.asked, src)
	return l.ev, l.err
}

// planRow is the plan's row for role with its column padding collapsed, for a
// test that asserts a row's cells rather than the whole table's alignment.
func planRow(plan, role string) string {
	for _, line := range strings.Split(plan, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == role {
			return strings.Join(f, " ")
		}
	}
	return ""
}

func poolFlags(pool string) *modelsFlags { return &modelsFlags{pool: &pool} }

func runPool(t *testing.T, root, lang, pool string, vals *certifyVals, load *fakeLoad, confirm *fakeConfirm) (poolSeating, int, bool, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	f := &modelsFlags{}
	if pool != "" {
		f = poolFlags(pool)
	}
	seating, exit, done := seatFromPool(context.Background(), "corral certify --local", root, lang, anyLangSeveral, f, vals.roles(),
		poolDeps{load: load.fn, confirm: confirm.fn, stdout: &out, stderr: &errb})
	return seating, exit, done, out.String(), errb.String()
}

func TestSeatFromPoolUnsetChangesNothing(t *testing.T) {
	root := poolEnv(t)
	vals := &certifyVals{mutant: "anthropic/claude-sonnet-5-5"}
	load, confirm := &fakeLoad{ev: sampleEvidence()}, &fakeConfirm{accepted: true, interactive: true}
	seating, exit, done, out, errs := runPool(t, root, "python", "", vals, load, confirm)
	if done || exit != 0 {
		t.Fatalf("done=%v exit=%d, want a door that carries on", done, exit)
	}
	if seating.SeatedBy != nil || seating.SeatEvidence != "" {
		t.Errorf("seating = %+v, want zero", seating)
	}
	if *vals != (certifyVals{mutant: "anthropic/claude-sonnet-5-5"}) {
		t.Errorf("flag values changed: %+v", *vals)
	}
	if len(load.asked) != 0 || confirm.calls != 0 || out != "" || errs != "" {
		t.Errorf("without --models nothing may be read, asked or printed: load=%v confirm=%d out=%q err=%q", load.asked, confirm.calls, out, errs)
	}
}

func TestSeatFromPoolWritesChosenSeatsIntoTheFlags(t *testing.T) {
	root := poolEnv(t)
	vals := &certifyVals{}
	load, confirm := &fakeLoad{ev: sampleEvidence()}, &fakeConfirm{accepted: true, interactive: true}
	seating, exit, done, out, errs := runPool(t, root, "python", samplePool, vals, load, confirm)
	if done || exit != 0 {
		t.Fatalf("done=%v exit=%d stderr=%q, want an accepted lineup to carry on", done, exit, errs)
	}
	want := certifyVals{
		derive: "anthropic/claude-sonnet-5-5", mutant: "anthropic/claude-sonnet-5-5",
		writer: "anthropic/claude-sonnet-5-5", critic: "codex:gpt-6",
	}
	if *vals != want {
		t.Errorf("flags = %+v, want each set to the chosen member as the operator typed it: %+v", *vals, want)
	}
	wantBy := map[string]string{
		modelrank.SeatGoalDeriver: "order", modelrank.SeatMutantGenerator: "evidence",
		modelrank.SeatTestWriter: "evidence", modelrank.SeatTestCritic: "order",
	}
	if fmt.Sprint(seating.SeatedBy) != fmt.Sprint(wantBy) {
		t.Errorf("SeatedBy = %v, want %v", seating.SeatedBy, wantBy)
	}
	wantEvidence := sampleSource + "; language python"
	if seating.SeatEvidence != wantEvidence {
		t.Errorf("SeatEvidence = %q, want %q", seating.SeatEvidence, wantEvidence)
	}
	wantPlan := strings.Join([]string{
		"seats (from --models " + samplePool + "):",
		"  goal-deriver      claude-sonnet-5-5  order     no measurement for this seat: taken from the order you named",
		"  mutant-generator  claude-sonnet-5-5  evidence  60 planted, 54 graded, 6 invalid, 24 survived over 6 runs",
		"  test-writer       claude-sonnet-5-5  evidence  193/195 survivors proven over 6 runs",
		"  test-critic       gpt-6 (codex)      order     no measurement for this seat: taken from the order you named",
		"evidence: " + sampleSource + "; language python",
		"grading rules: generations 40 to 43 across 6 scan(s) with evidence",
		"",
	}, "\n")
	if out != wantPlan {
		t.Errorf("plan:\n%s\nwant:\n%s", out, wantPlan)
	}
	if confirm.calls != 1 || confirm.plan != wantPlan {
		t.Errorf("confirm asked %d time(s) with plan %q, want once with the printed plan", confirm.calls, confirm.plan)
	}
	if len(load.asked) != 1 || load.asked[0] != (rankSource{Kind: rankSourceLedger}) {
		t.Errorf("evidence read from %v, want the machine ledger once", load.asked)
	}

	// A pin is the operator's: recorded as such, printed as such, and left
	// exactly as typed.
	vals = &certifyVals{critic: "off"}
	confirm = &fakeConfirm{accepted: true, interactive: true}
	seating, exit, done, out, errs = runPool(t, root, "python", samplePool, vals, &fakeLoad{ev: sampleEvidence()}, confirm)
	if done || exit != 0 {
		t.Fatalf("pinned critic: done=%v exit=%d stderr=%q", done, exit, errs)
	}
	if seating.SeatedBy[modelrank.SeatTestCritic] != "operator" || vals.critic != "off" {
		t.Errorf("pinned critic: SeatedBy=%v critic=%q, want operator and off", seating.SeatedBy, vals.critic)
	}
	if got := planRow(out, modelrank.SeatTestCritic); got != "test-critic off operator pinned by --critic-model" {
		t.Errorf("pinned critic row missing from the plan:\n%s", out)
	}
}

func TestSeatFromPoolRefusesBelowTheFloor(t *testing.T) {
	root := poolEnv(t)
	ev := sampleEvidence()
	// Writer evidence below the floor of five survivors attempted.
	var obs []modelrank.Observation
	for _, o := range ev.Obs {
		if o.Role != modelrank.SeatTestWriter {
			obs = append(obs, o)
		}
	}
	ev.Obs = append(obs, writerObs("claude-sonnet-5-5", "python", "r1", 3, 4))
	vals := &certifyVals{}
	confirm := &fakeConfirm{accepted: true, interactive: true}
	_, exit, done, out, errs := runPool(t, root, "python", samplePool, vals, &fakeLoad{ev: ev}, confirm)
	if exit != 2 || !done {
		t.Fatalf("exit=%d done=%v, want a refusal (2, done)", exit, done)
	}
	if confirm.calls != 0 {
		t.Errorf("a refused lineup must never be offered; confirm was called %d time(s)", confirm.calls)
	}
	if *vals != (certifyVals{}) {
		t.Errorf("a refusal changed the flags: %+v", *vals)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "pin --writer-model") {
		t.Errorf("plan must mark the unfilled seat and name its fix:\n%s", out)
	}
	if got := planRow(out, modelrank.SeatTestWriter); got != "test-writer — REFUSED no model in --models has 5+ survivors attempted as test-writer; pin --writer-model" {
		t.Errorf("refused row has the wrong shape: %q\n%s", got, out)
	}
	if !strings.Contains(errs, "nothing was spent") {
		t.Errorf("stderr must say nothing was spent: %q", errs)
	}
}

func TestSeatFromPoolNoEvidenceYet(t *testing.T) {
	root := poolEnv(t)
	src := "the machine ledger /state/ledger (no entries yet)"
	load := &fakeLoad{ev: rankEvidence{Source: src}}
	confirm := &fakeConfirm{accepted: true, interactive: true}
	vals := &certifyVals{}
	_, exit, done, out, _ := runPool(t, root, "python", samplePool, vals, load, confirm)
	if exit != 2 || !done || confirm.calls != 0 {
		t.Fatalf("exit=%d done=%v confirm=%d, want refused before any question", exit, done, confirm.calls)
	}
	if !strings.Contains(out, src) {
		t.Errorf("the refusal must name what was read (%q):\n%s", src, out)
	}
	if len(load.asked) != 1 || load.asked[0].Kind != rankSourceLedger {
		t.Errorf("load asked %v, want the ledger once and no other source", load.asked)
	}
	if *vals != (certifyVals{}) {
		t.Errorf("flags changed: %+v", *vals)
	}
}

func TestSeatFromPoolBadPool(t *testing.T) {
	root := poolEnv(t)
	cases := []struct{ pool, want string }{
		{"anthropic/claude-sonnet-5-5,,codex:gpt-6", "empty member"},
		{"anthropic/claude-sonnet-5-5,anthropic/claude-sonnet-5-5", `"anthropic/claude-sonnet-5-5" twice`},
		{"anthropic/claude-sonnet-5-5,off", `"off"`},
		{"anthropic/claude-sonnet-5-5,claude-haiku-5-5", `member "claude-haiku-5-5"`},
	}
	for _, c := range cases {
		load, confirm := &fakeLoad{ev: sampleEvidence()}, &fakeConfirm{accepted: true, interactive: true}
		vals := &certifyVals{}
		_, exit, done, out, errs := runPool(t, root, "python", c.pool, vals, load, confirm)
		if exit != 2 || !done {
			t.Errorf("%q: exit=%d done=%v, want 2 and done", c.pool, exit, done)
		}
		if !strings.Contains(errs, c.want) {
			t.Errorf("%q: stderr %q does not name the member (%s)", c.pool, errs, c.want)
		}
		if len(load.asked) != 0 || confirm.calls != 0 || out != "" || *vals != (certifyVals{}) {
			t.Errorf("%q: a bad pool must stop before evidence, plan or question: load=%v confirm=%d out=%q vals=%+v",
				c.pool, load.asked, confirm.calls, out, *vals)
		}
	}

	// A member whose credential is missing is refused up front, drawn or not.
	shadowMemberRunnable = func(rs agentbackend.ResolvedSeat) error {
		if rs.Spec == "claude-haiku-5-5" {
			return errors.New("ANTHROPIC_API_KEY is not set")
		}
		return nil
	}
	_, exit, done, _, errs := runPool(t, root, "python", samplePool, &certifyVals{}, &fakeLoad{ev: sampleEvidence()}, &fakeConfirm{})
	if exit != 2 || !done || !strings.Contains(errs, `member "anthropic/claude-haiku-5-5" cannot run`) || !strings.Contains(errs, "ANTHROPIC_API_KEY") {
		t.Errorf("credential: exit=%d done=%v stderr=%q", exit, done, errs)
	}
}

func TestSeatFromPoolAllPinned(t *testing.T) {
	root := poolEnv(t)
	pinned := certifyVals{derive: "a", mutant: "b", writer: "c", critic: "off"}
	vals := pinned
	load, confirm := &fakeLoad{ev: sampleEvidence()}, &fakeConfirm{accepted: true, interactive: true}
	seating, exit, done, out, _ := runPool(t, root, "python", samplePool, &vals, load, confirm)
	if done || exit != 0 {
		t.Fatalf("done=%v exit=%d, want the door to carry on", done, exit)
	}
	if confirm.calls != 0 || len(load.asked) != 0 {
		t.Errorf("every seat is pinned: nothing to propose, so no evidence and no question (load=%v confirm=%d)", load.asked, confirm.calls)
	}
	for _, r := range vals.roles() {
		if seating.SeatedBy[r.role] != "operator" {
			t.Errorf("SeatedBy[%s] = %q, want operator", r.role, seating.SeatedBy[r.role])
		}
	}
	if vals != pinned || seating.SeatEvidence != "" || out != "" {
		t.Errorf("vals=%+v evidence=%q out=%q, want the pins untouched, no evidence and no plan", vals, seating.SeatEvidence, out)
	}
}

func TestSeatFromPoolNoTerminal(t *testing.T) {
	root := poolEnv(t)
	vals := &certifyVals{critic: "off"}
	confirm := &fakeConfirm{interactive: false}
	_, exit, done, out, _ := runPool(t, root, "python", samplePool, vals, &fakeLoad{ev: sampleEvidence()}, confirm)
	if exit != 0 || !done {
		t.Fatalf("exit=%d done=%v, want 0 and done: no terminal means nothing runs", exit, done)
	}
	want := "to run this lineup: --derive-model anthropic/claude-sonnet-5-5 --mutant-model anthropic/claude-sonnet-5-5 --writer-model anthropic/claude-sonnet-5-5\n"
	if !strings.HasSuffix(out, want) {
		t.Errorf("stdout must end with the explicit flags for the proposed (not pinned) seats:\n%s\nwant suffix %q", out, want)
	}
	if !strings.HasPrefix(out, "seats (from --models ") {
		t.Errorf("the plan must come first:\n%s", out)
	}
	if *vals != (certifyVals{critic: "off"}) {
		t.Errorf("flags changed: %+v", *vals)
	}
}

func TestSeatFromPoolDeclined(t *testing.T) {
	root := poolEnv(t)
	vals := &certifyVals{}
	_, exit, done, out, _ := runPool(t, root, "python", samplePool, vals, &fakeLoad{ev: sampleEvidence()},
		&fakeConfirm{accepted: false, interactive: true})
	if exit != 0 || !done {
		t.Fatalf("exit=%d done=%v, want 0 and done", exit, done)
	}
	if !strings.HasSuffix(out, "not run; nothing was spent\n") {
		t.Errorf("stdout must end with the decline:\n%s", out)
	}
	if *vals != (certifyVals{}) {
		t.Errorf("a declined lineup changed the flags: %+v", *vals)
	}
}

func TestSeatFromPoolAlias(t *testing.T) {
	root := poolEnv(t)
	t.Setenv(models.EnvInline, `{"edge": {"provider": "ollama", "model": "localmodel-a:1", "endpoint": "http://127.0.0.1:11434"}}`)
	var obs []modelrank.Observation
	obs = append(obs, genObs("localmodel-a:1", "python", "r1", "r2", "r3", "r4", "r5")...)
	for _, r := range []string{"r1", "r2", "r3", "r4", "r5"} {
		obs = append(obs, writerObs("localmodel-a:1", "python", r, 2, 2))
	}
	vals := &certifyVals{derive: "anthropic/claude-haiku-5-5", critic: "off"}
	confirm := &fakeConfirm{accepted: true, interactive: true}
	seating, exit, done, out, errs := runPool(t, root, "python", "edge,anthropic/claude-haiku-5-5", vals,
		&fakeLoad{ev: rankEvidence{Obs: obs, Source: sampleSource}}, confirm)
	if done || exit != 0 {
		t.Fatalf("done=%v exit=%d stderr=%q out=%s", done, exit, errs, out)
	}
	if vals.writer != "edge" || vals.mutant != "edge" {
		t.Errorf("writer=%q mutant=%q, want the alias as typed (the door's own resolution rewrites it)", vals.writer, vals.mutant)
	}
	if seating.SeatedBy[modelrank.SeatTestWriter] != "evidence" {
		t.Errorf("SeatedBy = %v, want the writer seated by evidence under its concrete model", seating.SeatedBy)
	}
	if got := planRow(out, modelrank.SeatTestWriter); got != "test-writer localmodel-a:1 evidence 10/10 survivors proven over 5 runs" {
		t.Errorf("the plan must show the concrete model the evidence ranks:\n%s", out)
	}
}

func TestSeatFromPoolEvidenceUnreadable(t *testing.T) {
	root := poolEnv(t)
	confirm := &fakeConfirm{accepted: true, interactive: true}
	vals := &certifyVals{}
	_, exit, done, out, errs := runPool(t, root, "python", samplePool, vals,
		&fakeLoad{err: errors.New("the machine ledger /state/ledger: chain does not verify")}, confirm)
	if exit != 2 || !done {
		t.Fatalf("exit=%d done=%v, want a refusal", exit, done)
	}
	if !strings.Contains(errs, "chain does not verify") {
		t.Errorf("stderr must carry the loader's error: %q", errs)
	}
	if out != "" || confirm.calls != 0 || *vals != (certifyVals{}) {
		t.Errorf("nothing may be proposed: out=%q confirm=%d vals=%+v", out, confirm.calls, *vals)
	}
}

// The language line: a scan of one language ranks that language when the
// evidence records languages; a scan of several (shadowpool.LangAny, the
// marker certify --repo already passes) ranks across all of them; evidence
// that records no language is across all languages and says so.
func TestSeatFromPoolLanguageLine(t *testing.T) {
	root := poolEnv(t)
	stripped := sampleEvidence()
	for i := range stripped.Obs {
		stripped.Obs[i].Lang = ""
	}
	cases := []struct {
		lang string
		ev   rankEvidence
		want string
	}{
		{shadowpool.LangAny, sampleEvidence(), "; all languages (this scan has more than one)\n"},
		{"python", stripped, "; all languages (the evidence records none)\n"},
	}
	for _, c := range cases {
		confirm := &fakeConfirm{accepted: true, interactive: true}
		seating, exit, done, out, errs := runPool(t, root, c.lang, samplePool, &certifyVals{}, &fakeLoad{ev: c.ev}, confirm)
		if done || exit != 0 {
			t.Errorf("%s: done=%v exit=%d stderr=%q\n%s", c.lang, done, exit, errs, out)
			continue
		}
		if !strings.Contains(out, "evidence: "+sampleSource+c.want) || !strings.HasSuffix(seating.SeatEvidence, strings.TrimSuffix(c.want, "\n")) {
			t.Errorf("%s: evidence line wrong (SeatEvidence %q):\n%s", c.lang, seating.SeatEvidence, out)
		}
	}

	// Across languages means one ranking per seat over all of them: a writer
	// with 3 survivors attempted in python and 3 in go has 6, above the
	// floor, though neither language alone clears it.
	split := sampleEvidence()
	var obs []modelrank.Observation
	for _, o := range split.Obs {
		if o.Role != modelrank.SeatTestWriter {
			obs = append(obs, o)
		}
	}
	split.Obs = append(obs, writerObs("claude-sonnet-5-5", "python", "r1", 3, 3), writerObs("claude-sonnet-5-5", "go", "r2", 3, 3))
	seating, exit, done, out, errs := runPool(t, root, shadowpool.LangAny, samplePool, &certifyVals{}, &fakeLoad{ev: split},
		&fakeConfirm{accepted: true, interactive: true})
	if done || exit != 0 || seating.SeatedBy[modelrank.SeatTestWriter] != "evidence" {
		t.Errorf("languages pooled: done=%v exit=%d SeatedBy=%v, want the writer seated from both languages (stderr %q)\n%s", done, exit, seating.SeatedBy, errs, out)
	}

	// No language at all is a door's bug, not a choice to guess at.
	_, exit, done, _, errs = runPool(t, root, "", samplePool, &certifyVals{}, &fakeLoad{ev: sampleEvidence()}, &fakeConfirm{})
	if exit != 2 || !done || !strings.Contains(errs, "language") {
		t.Errorf("empty language: exit=%d done=%v stderr=%q, want a refusal naming it", exit, done, errs)
	}
}

// A scan with no single language says WHY on the evidence line, in the
// door's own words, and a door that gives no reason is refused: the line is
// the operator's record of what the ranking read, and one door's reason
// printed by another is a false statement.
func TestSeatFromPoolSaysWhyThereIsNoSingleLanguage(t *testing.T) {
	root := poolEnv(t)
	for _, c := range []struct {
		why  anyLangReason
		want string
	}{
		{anyLangSeveral, "all languages (this scan has more than one)"},
		{anyLangReview, "all languages (a review is not ranked by language)"},
		{anyLangUnknown, "all languages (no single language is known for this check)"},
	} {
		var out, errb bytes.Buffer
		_, exit, done := seatFromPool(context.Background(), "corral x", root, shadowpool.LangAny, c.why, poolFlags(samplePool), (&certifyVals{}).roles(),
			poolDeps{load: (&fakeLoad{ev: sampleEvidence()}).fn, confirm: (&fakeConfirm{accepted: true, interactive: true}).fn, stdout: &out, stderr: &errb})
		if exit != 0 || done || !strings.Contains(out.String(), "; "+c.want+"\n") {
			t.Errorf("%q: exit=%d done=%v stderr=%q\n%s", c.why, exit, done, errb.String(), out.String())
		}
	}
	var out, errb bytes.Buffer
	_, exit, done := seatFromPool(context.Background(), "corral x", root, shadowpool.LangAny, "", poolFlags(samplePool), (&certifyVals{}).roles(),
		poolDeps{load: (&fakeLoad{ev: sampleEvidence()}).fn, confirm: (&fakeConfirm{accepted: true, interactive: true}).fn, stdout: &out, stderr: &errb})
	if exit != 2 || !done || !strings.Contains(errb.String(), "reason") || out.Len() != 0 {
		t.Errorf("no reason: exit=%d done=%v stderr=%q out=%q, want a refusal that shows no plan", exit, done, errb.String(), out.String())
	}
}

func TestRegisterModelsFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerModelsFlags(fs, "the pool")
	if f.pool != nil {
		t.Fatalf("pool set before parsing: %q", *f.pool)
	}
	if err := fs.Parse([]string{"--models", "a,b"}); err != nil || f.pool == nil || *f.pool != "a,b" {
		t.Fatalf("parse: err=%v pool=%v", err, f.pool)
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerModelsFlags(fs, "the pool")
	if err := fs.Parse([]string{"--models", "a", "--models", "b"}); err == nil {
		t.Errorf("--models given twice was accepted; one would be silently dropped")
	}
}

func TestTerminalConfirmNotATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var out bytes.Buffer
	accepted, interactive, err := terminalConfirm(r, &out)("plan")
	if accepted || interactive || err != nil || out.Len() != 0 {
		t.Errorf("a pipe is not a terminal: accepted=%v interactive=%v err=%v out=%q", accepted, interactive, err, out.String())
	}
}
