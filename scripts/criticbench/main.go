// SPDX-License-Identifier: Elastic-2.0

// criticbench compares the test-critic's modes on suites with KNOWN answers:
// the tool loop (agentworker.RunCriticLoop), the typed single call
// (agentworker.RunCriticTyped), and the seat certify runs (agentworker.RunRole:
// typed, retried once, the loop as fallback). Each mode judges each fixture
// -runs times through a metered model; findings are graded against the
// fixture's answer key, which TestFixtureAnswerKeysAreExecuted checks by
// running the tests. Internal tooling: it lives in scripts/, not cmd/, so no
// user-facing CLI page is generated for it.
//
//	go run ./scripts/criticbench -model qwen2.5-coder:7b -runs 5 -out report.md
//	go run ./scripts/criticbench -model gemini-3.8-flash -runs 5 -out report.md
//
// A model name agentbackend.VendorOf recognises goes to that cloud vendor
// (and spends money, against that vendor's key); anything else is served by
// the local ollama daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/queue"
)

type mode struct {
	name string
	run  func(agentworker.Chatter, string) (string, []queue.Finding, error)
}

// modes: the tool loop alone, the typed call alone (one attempt, so its raw
// parse rate shows), and the seat certify runs (typed, retried once, the loop
// as fallback).
var modes = []mode{
	{"loop", agentworker.RunCriticLoop},
	{"typed", agentworker.RunCriticTyped},
	{"seat", func(c agentworker.Chatter, in string) (string, []queue.Finding, error) {
		return agentworker.RunRole(context.Background(), c, advpool.RoleTestCritic, in)
	}},
}

// selectModes keeps the modes named in a comma-separated list, in the order
// of modes; an empty list is every mode, and an unknown name is an error.
func selectModes(list string) ([]mode, error) {
	if strings.TrimSpace(list) == "" {
		return modes, nil
	}
	want := map[string]bool{}
	for _, n := range strings.Split(list, ",") {
		want[strings.TrimSpace(n)] = true
	}
	var out []mode
	for _, m := range modes {
		if want[m.name] {
			out = append(out, m)
			delete(want, m.name)
		}
	}
	for n := range want {
		return nil, fmt.Errorf("unknown mode %q (have loop, typed, seat)", n)
	}
	return out, nil
}

// result is one critic run on one fixture.
type result struct {
	fixture, mode       string
	flagged             []string
	right, missed, fals int
	calls               int64
	in, out             int64
	incomplete          bool
	unkeyed             bool
	planted             bool
	other               int // flags on unplanted tests of a planted file
	judged              int // tests the typed answer judged; -1 when it ended on no typed answer
	err                 string
}

var testName = regexp.MustCompile(`Test[A-Za-z0-9_]+`)

// grade turns findings into the set of named tests they flag (by Target or
// TestSelector) and scores it against the answer key. A finding naming no
// test in the file is a false alarm: it flagged something that is not there.
func grade(f fixture, findings []queue.Finding) (flagged []string, right, missed, fals int) {
	flagged, right, missed, fals, _ = gradeFull(f, findings)
	return flagged, right, missed, fals
}

// gradeFull is grade plus other: on a planted file, flags on tests in the
// file that were not planted. Nobody keyed those, so they are neither right
// nor false.
func gradeFull(f fixture, findings []queue.Finding) (flagged []string, right, missed, fals, other int) {
	inFile := map[string]bool{}
	for _, n := range f.all {
		inFile[n] = true
	}
	truth := map[string]bool{}
	for _, n := range f.vacuous {
		truth[n] = true
	}
	seen := map[string]bool{}
	for _, fd := range findings {
		names := testName.FindAllString(fd.Target+" "+fd.TestSelector, -1)
		hit := ""
		for _, n := range names {
			if inFile[n] {
				hit = n
				break
			}
		}
		if hit == "" {
			fals++
			continue
		}
		if seen[hit] {
			continue
		}
		seen[hit] = true
		flagged = append(flagged, hit)
		if f.unkeyed {
			continue
		}
		if f.planted && !truth[hit] {
			other++
			continue
		}
		if truth[hit] {
			right++
		} else {
			fals++
		}
	}
	if !f.unkeyed {
		missed = len(f.vacuous) - right
	}
	sort.Strings(flagged)
	return flagged, right, missed, fals, other
}

func main() {
	model := flag.String("model", "", "the critic model (required): a cloud model name goes to its vendor, anything else to the local ollama daemon")
	url := flag.String("url", envOr("OLLAMA_URL", "http://127.0.0.1:11434"), "ollama base URL")
	runs := flag.Int("runs", 5, "runs per fixture per mode")
	out := flag.String("out", "", "write the markdown report here as well as to stdout")
	files := flag.String("files", "", "comma-separated real Go source files to bench INSTEAD of the keyed fixtures; each needs its _test.go beside it, and has no answer key")
	modeList := flag.String("modes", "", "comma-separated modes to run (loop, typed, seat); empty runs all three")
	show := flag.String("show", "", "with -files: write each benched test file (planted, if -plant) into this directory and exit without calling a model")
	plant := flag.Int("plant", 0, "with -files: plant this many vacuous tests in each file's test file (in memory, checked with go test -overlay) and key on them")
	flag.Parse()
	if (*model == "" && *show == "") || *runs < 1 {
		fmt.Fprintln(os.Stderr, "criticbench: -model is required and -runs must be at least 1")
		os.Exit(2)
	}
	runModes, err := selectModes(*modeList)
	if err != nil {
		fmt.Fprintln(os.Stderr, "criticbench:", err)
		os.Exit(2)
	}
	bench := fixtures
	if *files != "" {
		bench = nil
		for _, p := range strings.Split(*files, ",") {
			load := realFixture
			if *plant > 0 {
				load = func(src string) (fixture, error) { return plantVacuous(src, *plant) }
			}
			f, err := load(strings.TrimSpace(p))
			if err != nil {
				fmt.Fprintln(os.Stderr, "criticbench:", err)
				os.Exit(2)
			}
			bench = append(bench, f)
		}
	}
	if *show != "" {
		for _, f := range bench {
			out := filepath.Join(*show, strings.ReplaceAll(strings.TrimSuffix(f.name, " (planted)"), "/", "_")+"_test.go")
			if err := os.WriteFile(out, []byte(f.tests), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "criticbench:", err)
				os.Exit(1)
			}
			fmt.Printf("%s: planted %v -> %s\n", f.name, f.vacuous, out)
		}
		return
	}
	backend := agentbackend.NewOllamaBackend(*url, *model)
	if agentbackend.VendorOf(*model) != "" {
		b, err := agentbackend.ForModel(*model)
		if err != nil {
			fmt.Fprintln(os.Stderr, "criticbench:", err)
			os.Exit(2)
		}
		backend = b
	}
	var results []result
	for _, f := range bench {
		instr := advpool.CriticInstruction(advpool.RunSpec{
			Goal: f.goal, CodePath: f.codePath, Code: f.code, DevTestPath: f.testPath, DevTestCode: f.tests,
		})
		for _, m := range runModes {
			for i := 0; i < *runs; i++ {
				meter := &agentbackend.UsageMeter{}
				rec := &recorder{inner: agentbackend.AsChatterMetered(backend, meter)}
				summary, findings, err := m.run(rec, instr)
				in, o, calls := meter.Totals()
				r := result{fixture: f.name, mode: m.name, unkeyed: f.unkeyed, planted: f.planted, calls: calls, in: in, out: o,
					incomplete: strings.HasPrefix(summary, agentworker.CriticIncompletePrefix), judged: -1}
				if m.name != "loop" {
					if j, ok := coverage(f, rec.last); ok {
						r.judged = j
					}
				}
				if err != nil {
					r.err = err.Error()
				} else {
					r.flagged, r.right, r.missed, r.fals, r.other = gradeFull(f, findings)
				}
				fmt.Fprintf(os.Stderr, "%s/%s run %d: flagged %v (right %d, missed %d, false %d), judged %d of %d, %d calls, %d in / %d out%s\n",
					f.name, m.name, i+1, r.flagged, r.right, r.missed, r.fals, r.judged, len(f.all), r.calls, r.in, r.out, map[bool]string{true: ", INCOMPLETE", false: ""}[r.incomplete])
				results = append(results, r)
			}
		}
	}
	total := map[string]int{}
	for _, f := range bench {
		total[f.name] = len(f.all)
	}
	report := render(*model, *runs, results, total)
	fmt.Print(report)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(report), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "criticbench:", err)
			os.Exit(1)
		}
	}
}

// render totals each (fixture, mode) over its runs. Stability is the number
// of DISTINCT flagged sets across the runs: 1 means every run gave the same
// answer. Raw counts throughout, never percentages, at this sample size.
func render(model string, runs int, results []result, total map[string]int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# criticbench: %s, %d run(s) per fixture per mode\n\n", model, runs)
	b.WriteString("| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	type key struct{ f, m string }
	var order []key
	agg := map[key]*result{}
	sets := map[key]map[string]bool{}
	inc, errs := map[key]int{}, map[key]int{}
	minJudged := map[key]int{}
	for _, r := range results {
		k := key{r.fixture, r.mode}
		if agg[k] == nil {
			agg[k] = &result{}
			sets[k] = map[string]bool{}
			order = append(order, k)
		}
		a := agg[k]
		a.right += r.right
		a.missed += r.missed
		a.fals += r.fals
		a.other += r.other
		a.calls += r.calls
		a.in += r.in
		a.out += r.out
		if r.incomplete {
			inc[k]++
		}
		if r.judged >= 0 {
			if v, ok := minJudged[k]; !ok || r.judged < v {
				minJudged[k] = r.judged
			}
		}
		if r.err != "" {
			errs[k]++
			continue
		}
		sets[k][strings.Join(r.flagged, ",")] = true
	}
	unkeyed, planted := map[string]bool{}, map[string]bool{}
	for _, r := range results {
		if r.unkeyed {
			unkeyed[r.fixture] = true
		}
		if r.planted {
			planted[r.fixture] = true
		}
	}
	for _, k := range order {
		a := agg[k]
		right, missed := fmt.Sprint(a.right), fmt.Sprint(a.missed)
		if unkeyed[k.f] {
			right, missed = "no key", "no key"
		}
		judged := "n/a"
		if v, ok := minJudged[k]; ok {
			judged = fmt.Sprintf("%d / %d", v, total[k.f])
		}
		other := "n/a"
		if planted[k.f] {
			other = fmt.Sprint(a.other)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s | %d | %d | %d | %s | %d | %d | %d |\n",
			k.f, k.m, right, missed, a.fals, other, inc[k], errs[k], len(sets[k]), judged, a.calls, a.in, a.out)
	}
	b.WriteString("\nright/missed/false are summed over the runs, against an answer key checked by execution (TestFixtureAnswerKeysAreExecuted). A real file has no key: only a flag naming a test that is not in the file counts as false there. A planted file is keyed on its planted tests only; flags on its other tests are counted apart.\n")
	writeAgreement(&b, results, runs)
	return b.String()
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// writeAgreement lists, for each real (unkeyed) file, every test any mode
// flagged and how many of its runs flagged it. With no key this is what a
// person adjudicates: where the modes agree, where one stands alone.
func writeAgreement(b *strings.Builder, results []result, runs int) {
	type key struct{ f, test string }
	count := map[key]map[string]int{}
	var files, modeNames []string
	seenFile, seenMode := map[string]bool{}, map[string]bool{}
	for _, r := range results {
		if !r.unkeyed && !r.planted {
			continue
		}
		if !seenFile[r.fixture] {
			seenFile[r.fixture] = true
			files = append(files, r.fixture)
		}
		if !seenMode[r.mode] {
			seenMode[r.mode] = true
			modeNames = append(modeNames, r.mode)
		}
		for _, t := range r.flagged {
			k := key{r.fixture, t}
			if count[k] == nil {
				count[k] = map[string]int{}
			}
			count[k][r.mode]++
		}
	}
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## Flagged tests on real files (runs out of %d that flagged each)\n", runs)
	for _, f := range files {
		var tests []string
		for k := range count {
			if k.f == f {
				tests = append(tests, k.test)
			}
		}
		sort.Strings(tests)
		fmt.Fprintf(b, "\n### %s\n\n", f)
		if len(tests) == 0 {
			b.WriteString("No mode flagged any test.\n")
			continue
		}
		fmt.Fprintf(b, "| test | %s |\n|---|%s\n", strings.Join(modeNames, " | "), strings.Repeat("---|", len(modeNames)))
		for _, t := range tests {
			cells := make([]string, len(modeNames))
			for i, m := range modeNames {
				cells[i] = fmt.Sprint(count[key{f, t}][m])
			}
			fmt.Fprintf(b, "| %s | %s |\n", t, strings.Join(cells, " | "))
		}
	}
}
