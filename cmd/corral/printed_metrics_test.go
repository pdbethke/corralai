// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// printedRow says where a function's printed numbers go. Fields names the
// ledger fields ("<Row>.<Field>") the numbers come from; Exempt says, in a
// specific sentence, why a printed number is not a record field. A row has at
// least one; it has both when only some of a function's numbers are covered.
//
// Three Exempt prefixes carry meaning a reader can grep for:
//   - "GAP: <what>; fixed in Task N": a number that belongs on the record and
//     a later task of the record-complete plan puts it there.
//   - "UNCOVERED: <what>": a number that arguably belongs on the record and no
//     planned task covers; each is reported to the controller to rule on.
//     (None are left at present: every number once marked UNCOVERED is now a
//     GAP owned by a task.)
//   - "review metrics: phase 1b": plan ruling R5, review's printers.
//
// Every other Exempt names why the number is not a measurement of a run:
// usage text, an error message, a framing length, a key.
type printedRow struct {
	Fields []string
	Exempt string
}

// numericVerb matches a Printf verb that can print a number: d i o O b x X e E
// f F g G, and v (which prints whatever it is handed, a number included), with
// flags, an optional [n] argument index before or after the width, a width and
// a precision. %s and %q stay out: they print text.
var numericVerb = regexp.MustCompile(`%[-+# 0]*(\[[0-9]+\])?[0-9]*(\.[0-9]+)?(\[[0-9]+\])?[diobOxXeEfFgGv]`)

// fmtFormatted are the fmt calls whose format string sits at index 0 (Printf,
// Sprintf, Errorf) or 1 (Fprintf, Appendf). Errorf is included on purpose: it
// is not known here whether the error reaches a printer, so a row whose only
// hits are errors is exempted as "error message: ...".
var fmtFormatted = map[string]int{"Printf": 0, "Sprintf": 0, "Errorf": 0, "Fprintf": 1, "Appendf": 1}

// fmtPlain are the fmt calls that print their operands without a format; the
// value is the index of the first operand. A call counts when any operand is
// not a string literal.
var fmtPlain = map[string]int{"Print": 0, "Println": 0, "Sprint": 0, "Sprintln": 0, "Fprint": 1, "Fprintln": 1, "Append": 1, "Appendln": 1}

// strconvNumber are the strconv calls that turn a number into text.
var strconvNumber = map[string]bool{"Itoa": true, "FormatInt": true, "FormatUint": true, "FormatFloat": true,
	"AppendInt": true, "AppendUint": true, "AppendFloat": true}

// stringPieces collects every string literal reachable in e, resolving an
// identifier to a const or var string declared in the same package. It
// over-collects rather than under-collects: a format built from pieces is
// searched as the concatenation of the pieces it can see.
func stringPieces(e ast.Expr, decls map[string]ast.Expr, depth int) string {
	if depth > 8 {
		return ""
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if s, err := strconv.Unquote(x.Value); err == nil {
				return s
			}
		}
	case *ast.BinaryExpr:
		return stringPieces(x.X, decls, depth+1) + stringPieces(x.Y, decls, depth+1)
	case *ast.ParenExpr:
		return stringPieces(x.X, decls, depth+1)
	case *ast.Ident:
		if d, ok := decls[x.Name]; ok {
			return stringPieces(d, decls, depth+1)
		}
	}
	return ""
}

// packageStringDecls maps each package-level const or var with a single value
// to that value's expression, for stringPieces to resolve.
func packageStringDecls(files []*ast.File) map[string]ast.Expr {
	decls := map[string]ast.Expr{}
	for _, af := range files {
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				if len(vs.Names) == len(vs.Values) {
					for i, n := range vs.Names {
						decls[n.Name] = vs.Values[i]
					}
				}
			}
		}
	}
	return decls
}

// printsANumber reports whether any call under n prints, or may print, a number.
func printsANumber(n ast.Node, decls map[string]ast.Expr) bool {
	hit := false
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || hit {
			return !hit
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch id.Name {
		case "strconv":
			if strconvNumber[sel.Sel.Name] {
				hit = true
			}
		case "fmt":
			if idx, ok := fmtFormatted[sel.Sel.Name]; ok && len(call.Args) > idx {
				f := strings.ReplaceAll(stringPieces(call.Args[idx], decls, 0), "%%", "")
				if numericVerb.MatchString(f) {
					hit = true
				}
			} else if idx, ok := fmtPlain[sel.Sel.Name]; ok {
				for _, a := range call.Args[min(idx, len(call.Args)):] {
					if lit, ok := a.(*ast.BasicLit); !ok || lit.Kind != token.STRING {
						hit = true
					}
				}
			}
		}
		return !hit
	})
	return hit
}

// walkPackage returns "<label>/<file>:<name>" for every function, method and
// package-level func-literal variable in the given sources that prints a
// number. A closure is attributed to the function that holds it. src maps a
// file name to its source.
func walkPackage(t *testing.T, label string, src map[string]string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	var names []string
	for n := range src {
		names = append(names, n)
	}
	sort.Strings(names)
	parsed := map[string]*ast.File{}
	var files []*ast.File
	for _, n := range names {
		af, err := parser.ParseFile(fset, n, src[n], 0)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		parsed[n] = af
		files = append(files, af)
	}
	decls := packageStringDecls(files)
	found := map[string]bool{}
	for _, n := range names {
		for _, d := range parsed[n].Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body != nil && printsANumber(d.Body, decls) {
					found[label+"/"+n+":"+d.Name.Name] = true
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, sp := range d.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, v := range vs.Values {
						if _, ok := v.(*ast.FuncLit); ok && i < len(vs.Names) && printsANumber(v, decls) {
							found[label+"/"+n+":"+vs.Names[i].Name] = true
						}
					}
				}
			}
		}
	}
	return found
}

// corralPackages derives the scope: the module's own non-test packages that
// `go list -deps ./cmd/corral` reports, which is exactly the code that runs in
// a corral run. The other binaries (corral-wrangler and the rest) are out of
// scope: they are not part of an audit. Files for other build constraints are
// included too, so a platform-specific printer is not missed.
func corralPackages(t *testing.T) map[string]map[string]string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f",
		"{{.ImportPath}}|{{.Dir}}|{{join .GoFiles \",\"}}|{{join .IgnoredGoFiles \",\"}}", "github.com/pdbethke/corralai/cmd/corral").Output() // #nosec G204 -- fixed arguments
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	const mod = "github.com/pdbethke/corralai"
	pkgs := map[string]map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 4 || !strings.HasPrefix(parts[0], mod) {
			continue
		}
		label := strings.TrimPrefix(strings.TrimPrefix(parts[0], mod), "/")
		files := map[string]string{}
		for _, list := range parts[2:] {
			for _, f := range strings.Split(list, ",") {
				if f == "" || strings.HasSuffix(f, "_test.go") {
					continue
				}
				b, err := os.ReadFile(filepath.Join(parts[1], f)) // #nosec G304 -- this repo's own sources
				if err != nil {
					t.Fatal(err)
				}
				files[f] = string(b)
			}
		}
		pkgs[label] = files
	}
	return pkgs
}

func printingFuncs(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	for label, files := range corralPackages(t) {
		for k := range walkPackage(t, label, files) {
			found[k] = true
		}
	}
	return found
}

// TestWalkFindsEveryShapeOfPrintedNumber is the negative control for the walk
// itself: the table gate is only as good as what the walk can see.
func TestWalkFindsEveryShapeOfPrintedNumber(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string // names expected, "" none
	}{
		{"plain Fprintf", `package p; func A(w io.Writer, n int) { fmt.Fprintf(w, "%d", n) }`, []string{"A"}},
		{"literal concatenation", `package p; func A(w io.Writer, n int) { fmt.Fprintf(w, "a "+"%d b", n) }`, []string{"A"}},
		{"const format", `package p; const f = "n=%d"; func A(n int) string { return fmt.Sprintf(f, n) }`, []string{"A"}},
		{"var format", `package p; var f = "n=" + "%.2f"; func A(n float64) string { return fmt.Sprintf(f, n) }`, []string{"A"}},
		{"Fprintln of a variable", `package p; func A(w io.Writer, n int) { fmt.Fprintln(w, n) }`, []string{"A"}},
		{"Println of a variable", `package p; func A(n int) { fmt.Println("x", n) }`, []string{"A"}},
		{"Itoa", `package p; func A(n int) string { return "x" + strconv.Itoa(n) }`, []string{"A"}},
		{"FormatFloat", `package p; func A(n float64) string { return strconv.FormatFloat(n, 'f', 2, 64) }`, []string{"A"}},
		{"percent v", `package p; func A(w io.Writer, n int) { fmt.Fprintf(w, "%v\n", n) }`, []string{"A"}},
		{"indexed verb", `package p; func A(n int) string { return fmt.Sprintf("%[1]d", n) }`, []string{"A"}},
		{"percent X", `package p; func A(n int) string { return fmt.Sprintf("%X", n) }`, []string{"A"}},
		{"percent b and o", `package p; func A(n int) string { return fmt.Sprintf("%b", n) + fmt.Sprintf("%o", n) }`, []string{"A"}},
		{"Errorf", `package p; func A(n int) error { return fmt.Errorf("exit %d", n) }`, []string{"A"}},
		{"Appendf", `package p; func A(b []byte, n int) []byte { return fmt.Appendf(b, "%d", n) }`, []string{"A"}},
		{"package-level func var", `package p; var A = func(w io.Writer, n int) { fmt.Fprintf(w, "%d", n) }`, []string{"A"}},
		{"closure inside a function", `package p; func A(w io.Writer) { f := func(n int) { fmt.Fprintf(w, "%d", n) }; f(1) }`, []string{"A"}},
		{"method", `package p; type T struct{}; func (T) A(w io.Writer, n int) { fmt.Fprintf(w, "%d", n) }`, []string{"A"}},
		{"percent s only", `package p; func A(w io.Writer, s string) { fmt.Fprintf(w, "%s: %q\n", s, s); fmt.Fprintln(w, "literal"); fmt.Fprintf(w, "100%%\n") }`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := walkPackage(t, "p", map[string]string{"x.go": c.src})
			want := map[string]bool{}
			for _, w := range c.want {
				want["p/x.go:"+w] = true
			}
			if len(got) != len(want) {
				t.Fatalf("found %v, want %v", got, want)
			}
			for k := range want {
				if !got[k] {
					t.Fatalf("found %v, want %v", got, want)
				}
			}
		})
	}
}

// TestEveryPrintedNumberIsRecordedOrExempt is the gate behind AGENTS.md's "a
// real measurement, correctly computed, then dropped on the floor": a function
// that prints a number must name the ledger fields that carry it, or say why
// no field should. A new printing function fails here until someone decides.
// Scope: the module's packages that run in a corral run (corralPackages).
func TestEveryPrintedNumberIsRecordedOrExempt(t *testing.T) {
	found := printingFuncs(t)
	if len(found) < 40 {
		t.Fatalf("walk found only %d printing functions; the walk is broken", len(found))
	}
	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		row, ok := printedMetrics[k]
		if !ok {
			t.Errorf("%s prints a number and has no row in printedMetrics", k)
			continue
		}
		if len(row.Fields) == 0 && row.Exempt == "" {
			t.Errorf("%s: row has neither Fields nor Exempt", k)
		}
		for _, f := range row.Fields {
			r, fld, ok := strings.Cut(f, ".")
			if !ok {
				t.Errorf("%s: field %q is not <Row>.<Field>", k, f)
				continue
			}
			if _, ok := ledger.TierOf(r, fld); !ok {
				t.Errorf("%s: %s is not a recorded field (ledger.TierOf)", k, f)
			}
		}
	}
	for k := range printedMetrics {
		if !found[k] {
			t.Errorf("printedMetrics names %s, which the walk did not find", k)
		}
	}
}

const (
	exFmtHelper   = "formatting helper: prints a number its caller passes in; the caller's row carries the field"
	exDigestFrame = "length-prefix framing of a digest input, not a measurement"
	exReview      = "review metrics: phase 1b"
	exTruncation  = "display truncation count of a printed list; the listed items are the fields"
)

func gap(what string, task string) string { return "GAP: " + what + "; fixed in " + task }

// printedMetrics is keyed "<package dir>/<file>:<func>". Later tasks that
// record a number add or change the row of every function that prints it.
var printedMetrics = map[string]printedRow{
	"cmd/corral/brief.go:buildBrief":  {Exempt: exTruncation + " (--max-items): the entries hold the items"},
	"cmd/corral/brief.go:spanText":    {Fields: []string{"MutantRow.SpanStart", "MutantRow.SpanEnd"}},
	"cmd/corral/brief.go:renderBrief": {Fields: []string{"Row.KillRate", "Row.Survivors", "Row.ProvenMissed"}},

	"cmd/corral/certify.go:runCertify":                        {Exempt: "key: the id and head hash of a certified build on the brain, not a measurement"},
	"cmd/corral/certify_adversarial.go:runCertifyAdversarial": {Exempt: "key and error message: a brain run id and the poll timeout, not a measurement"},
	"cmd/corral/certify_adversarial.go:renderAdvVerdict": {Fields: []string{
		"Row.KillRate", "Row.MutantsGraded", "Row.MutantsPlanted", "Row.MutantsDistinct", "Row.Survivors", "Row.MutantsInvalid",
		"Row.ProvenMissed", "Row.SymbolsProbed", "Row.Symbols", "Row.DecisionsProbed", "Row.Decisions",
		"Row.RegionsProbed", "Row.RegionsTotal", "Row.DroppedRegions", "Row.VacuousFindings", "Row.VerdictJSON"},
		Exempt: gap("the unmeasured-mutant, duplicate-mutant and ungraded-writer-seat counts, which exist only in Row.VerdictJSON, which is source and withheld without --push-source", "Task 8b")},

	"cmd/corral/certify_local.go:runCertifyLocalAs": {Fields: []string{"Row.MutantsPlanted", "Row.MutantsFrom"},
		Exempt: "tape beat count describes the --record-tape file this run writes, an output artifact; record id is a key"},
	"cmd/corral/certify_local.go:auditOneFile": {Fields: []string{"SeatRow.Shard", "SeatRow.Shadow", "SeatRow.ShadowDrawn", "Row.Symbols"},
		Exempt: gap("the swarm width (concurrent model calls) the run was given", "Task 5")},
	"cmd/corral/certify_local.go:buildJailWiring":          {Fields: []string{"Row.SharedDirs"}},
	"cmd/corral/certify_local_criticscore.go:Record":       {Exempt: "formats a record id and finding id into a key string, not a measurement"},
	"cmd/corral/certify_local_drive.go:driveLocalRun":      {Exempt: "error message: consecutive tick errors of the local driver before it gives up"},
	"cmd/corral/certify_local_drive.go:runReadyTasks":      {Exempt: "formats a worker name from a loop index, not a measurement"},
	"cmd/corral/certify_local_tape.go:renderMatrixSummary": {Fields: []string{"MutantRow.KilledBy", "MutantRow.Outcome"}},
	"cmd/corral/certify_local_tape.go:renderBeat":          {Fields: []string{"Row.MutantsPlanted", "Row.MutantsGraded"}},

	"cmd/corral/certify_repo.go:runCertifyRepo": {Fields: []string{
		"ScanRow.Top", "ScanRow.Candidates", "ScanRow.Audited", "ScanRow.TotalFiles", "ScanRow.DiffBase", "ScanRow.AllCandidates",
		"Row.PriorsApplied", "Row.MutantsFrom", "Row.Evidence", "ScanRow.ModelCalls"},
		Exempt: gap("mutant scoring concurrency and per-file worker count", "Task 5") + "; " + gap("the excluded-file count of the walk line, no ledger field holds exclusions by reason", "Task 8b") + "; counts in the 'ledger: entry written' and 'pushed' lines repeat the rows the entry holds"},
	"cmd/corral/certify_repo.go:goalCacheDisclosureLine": {Fields: []string{"Row.GoalsDerived", "Row.GoalReused"}},
	"cmd/corral/certify_repo.go:pickPreflightLanguage":   {Fields: []string{"ScanRow.PreflightNote"}},
	"cmd/corral/certify_repo.go:selectionCmdDigest":      {Exempt: exDigestFrame},
	"cmd/corral/certify_repo.go:noEvidenceSourcesNote":   {Exempt: "GAP: per-language count of sources whose test landing is not built; shown as a selection note, recorded only as free text if at all; fixed in Task 8b"},
	"cmd/corral/certify_repo.go:printPreflightReport": {Fields: []string{"ScanRow.PreflightRan", "ScanRow.PreflightNote", "Row.Uncovered"},
		Exempt: "GAP: the counts of files executed at least once and files not measured by the coverage preflight; fixed in Task 8b"},
	"cmd/corral/certify_repo.go:candidacySummaryLine":   {Fields: []string{"Row.Evidence", "Row.ImportOnly", "Row.Uncovered", "Row.Disposition"}},
	"cmd/corral/certify_repo.go:printEvidencePairings":  {Fields: []string{"Row.CoveringUnits", "Row.CoveringTests"}, Exempt: exTruncation},
	"cmd/corral/certify_repo.go:printSearchPairings":    {Exempt: exTruncation},
	"cmd/corral/certify_repo.go:printWouldAudit":        {Exempt: "dry-run inventory count, no run happens"},
	"cmd/corral/certify_repo.go:printExclusions":        {Exempt: "GAP: excluded-file counts by exclusion reason; the ledger holds audited files and totals, not exclusions by reason; fixed in Task 8b"},
	"cmd/corral/certify_repo.go:selectionRuleBreakdown": {Fields: []string{"MutantRow.SelectionRule"}},
	"cmd/corral/certify_repo.go:pluralCount":            {Exempt: exFmtHelper},
	"cmd/corral/certify_repo.go:redUnitsNames":          {Exempt: exTruncation},
	"cmd/corral/certify_repo.go:cautionDetail":          {Fields: []string{"Row.SelectionUnreported", "Row.SelectionExcluded"}},
	"cmd/corral/certify_repo.go:printWeakFile": {Fields: []string{
		"Row.Survivors", "Row.ProvenMissed", "Row.KillRate", "Row.SelectedTests", "Row.SuiteTests", "TestsPerMutantSpread.Min",
		"TestsPerMutantSpread.Median", "TestsPerMutantSpread.Max", "Row.PriorsApplied"}},
	"cmd/corral/certify_repo.go:flakyBaselineNote": {Fields: []string{"Row.BaselineFlaky"}},
	"cmd/corral/certify_repo.go:printRepoReport": {Fields: []string{
		"Row.KillRate", "ScanRow.Audited", "ScanRow.Candidates", "Row.Disposition", "Row.ImportOnly", "Row.Uncovered", "Row.TimedOut",
		"Row.TestWriterFailed", "Row.PoolTestUnsound", "Row.ProvenMissed", "Row.Survivors", "Row.CacheHit", "Row.MinKillRate",
		"Row.MaxProvenMissed", "Row.SelectedTests", "Row.SuiteTests"}},
	"cmd/corral/certify_repo.go:resolveScanWorkers":    {Exempt: gap("the swarm width (scan workers) the run resolved", "Task 5")},
	"cmd/corral/certify_repo.go:concurrencyDisclosure": {Fields: []string{"Row.Trees", "Row.ConcurrencyNote"}},
	"cmd/corral/certify_repo.go:baselineRedPhrase":     {Fields: []string{"Row.SelectionExcluded", "Row.SelectionExcludedRed"}},
	"cmd/corral/certify_repo.go:redUnitsPhrase":        {Fields: []string{"ScanRow.UnitPass"}},
	"cmd/corral/certify_repo.go:printLanguageProfile":  {Exempt: "GAP: per-language counts of auditable, unpaired and ambiguous source files; the ledger holds audited files, not the language inventory; fixed in Task 8b"},
	"cmd/corral/certify_repo.go:writerModeDisclosure": {Fields: []string{"Row.WriterMode", "ModelCallRow.Calls"},
		Exempt: "GAP: the writer attempts-per-survivor spread (min, median, max) and the count of ungraded writer seats; fixed in Task 8b"},
	"cmd/corral/certify_repo.go:examConfidenceLine": {Fields: []string{
		"Row.KillRate", "Row.MutantsDistinct", "Row.SymbolsProbed", "Row.Symbols", "Row.DecisionsProbed", "Row.Decisions"},
		Exempt: "GAP: the count of mutants the exam could not place (ExamCoverage.Unplaced); fixed in Task 8b"},
	"cmd/corral/certify_repo.go:writerPairLine": {Fields: []string{
		"Row.ChallengerMutants", "Row.ChallengerSurvivedWriter", "Row.ChallengerSurvivedShadow", "Row.ChallengerShared",
		"Row.ChallengerUnion", "Row.ChallengerJaccard"}},
	"cmd/corral/certify_repo_transparency.go:uploadToTransparencyLog": {Fields: []string{"ScanRow.RekorLogIndex"}},
	"cmd/corral/certify_repo_units.go:collectUnits": {Fields: []string{"ScanRow.UnitPass"},
		Exempt: gap("the unit worker count and evidence timeout", "Task 5")},
	"cmd/corral/certify_repo_units.go:unitsCmdDigest": {Exempt: exDigestFrame},
	"cmd/corral/certify_repo_units.go:printUnitPass": {Fields: []string{"ScanRow.UnitPass"},
		Exempt: gap("the unit worker count", "Task 5")},
	"cmd/corral/certify_repo_units.go:printUnitGrading": {Fields: []string{"ScanRow.UnitPass"}},

	"cmd/corral/cost_line.go:costLine":         {Fields: []string{"ModelCallRow.Calls", "ModelCallRow.InputTokens", "ModelCallRow.OutputTokens", "ModelCallRow.UnmeteredCalls", "ModelCallRow.CachedInputTokens"}},
	"cmd/corral/cost_line.go:abbreviateTokens": {Exempt: exFmtHelper},
	"cmd/corral/cost_line.go:budgetLine": {Fields: []string{"ScanRow.ModelCalls", "ScanRow.InputTokens", "ScanRow.OutputTokens"},
		Exempt: gap("the --max-tokens cap and whether it was reached", "Task 5")},

	"cmd/corral/doctor.go:runDoctor":       {Exempt: "doctor result: how many checks failed, no audit run to record into"},
	"cmd/corral/doctor.go:probeDeriveSeat": {Exempt: "doctor writes no ledger entry"},

	"cmd/corral/ledger.go:runLedgerCheckpoint": {Fields: []string{"Checkpoint.Entries"}},
	"cmd/corral/ledger.go:runLedgerPush":       {Exempt: "ledger push result: counts of entries pushed, skipped and left out, the operation's own output; the rows are the record"},
	"cmd/corral/matrix.go:runMatrix":           {Fields: []string{"MutantRow.KilledBy", "MutantRow.Outcome"}},

	"cmd/corral/models_rank.go:printRankReport":       {Fields: []string{"SeatRow.Catches", "SeatRow.Opportunities", "SeatRow.ParseRetries"}, Exempt: "registry model count and --min-runs threshold are the operator's inputs"},
	"cmd/corral/models_rank.go:rankMetricCell":        {Fields: []string{"SeatRow.Catches", "SeatRow.Opportunities"}},
	"cmd/corral/models_rank.go:bugcatchRankEvidence":  {Exempt: "formats a record id into an evidence key, not a measurement"},
	"cmd/corral/models_rank.go:warehouseRankEvidence": {Exempt: "formats a scan id into an evidence key, not a measurement"},
	"cmd/corral/mutantset.go:report":                  {Exempt: "count of files written to the --record-mutants file, an output artifact; the mutants themselves are MutantRow"},

	"cmd/corral/review.go:printReview":              {Exempt: exReview},
	"cmd/corral/review.go:runReviewRun":             {Exempt: exReview},
	"cmd/corral/review_churn.go:printChurn":         {Exempt: exReview},
	"cmd/corral/review_plan.go:runReviewPlan":       {Exempt: exReview},
	"cmd/corral/review_recheck.go:printRecheck":     {Exempt: exReview},
	"cmd/corral/review_recheck.go:runReviewRecheck": {Exempt: exReview},

	"cmd/corral/run_bounds.go:evidenceCutNote": {Fields: []string{"ScanRow.UnitPass"}},

	"cmd/corral/scans.go:runScansList":          {Fields: []string{"ScanRow.Audited", "ScanRow.Candidates", "Row.CacheHit", "Row.KillRate"}},
	"cmd/corral/scans.go:runScansShow":          {Fields: []string{"Row.Survivors", "Row.ProvenMissed", "Row.KillRate", "ScanRow.RekorLogIndex"}},
	"cmd/corral/scans.go:authoredTestOutcome":   {Fields: []string{"Row.Survivors"}},
	"cmd/corral/scans.go:scanFileSelectionWith": {Fields: []string{"Row.TestSelection", "Row.SelectedTests", "Row.SuiteTests", "Row.SelectionUnreported", "TestsPerMutantSpread.Min", "TestsPerMutantSpread.Max"}},
	"cmd/corral/scans.go:excludedSuffix":        {Fields: []string{"Row.SelectionExcluded"}},
	"cmd/corral/scans.go:formatKillRate":        {Fields: []string{"Row.KillRate"}},
	"cmd/corral/scans.go:denominatorNote":       {Fields: []string{"Row.MutantsGraded", "Row.MutantsInvalid", "Row.DroppedRegions"}},
	"cmd/corral/scans.go:scanFileBudget":        {Fields: []string{"Row.MutantBudget", "Row.MutantBudgetRule"}},
	"cmd/corral/scans_ledger.go:EntryKind":      {Fields: []string{"Checkpoint.Entries"}, Exempt: exReview},

	"cmd/corral/scorecard.go:runScorecard":  {Fields: []string{"SeatRow.Catches", "SeatRow.Opportunities"}, Exempt: "RUNS is a count of SeatRow records per model and role, derived at read time, not a stored field"},
	"cmd/corral/scorecard.go:pctOrDash":     {Exempt: exFmtHelper},
	"cmd/corral/seal.go:runSealWithoutRepo": {Fields: []string{"Row.KillRate", "Row.Survivors", "Row.ProvenMissed", "Row.Trees"}},
	"cmd/corral/seal.go:runSealWithRepo":    {Fields: []string{"Row.KillRate", "Row.Survivors", "Row.ProvenMissed", "Row.Trees", "Row.Disposition", "Row.ImportOnly", "Row.Uncovered"}},
	"cmd/corral/seal.go:trimPercent":        {Exempt: exFmtHelper},
	"cmd/corral/seedskip.go:seedSkipsLine":  {Fields: []string{"ScanRow.SeedSkipped"}},
	"cmd/corral/setup.go:renderPickList":    {Exempt: "menu index of an interactive pick list, not a measurement"},

	"cmd/corral/shadow_draw.go:drawShadowSeats": {Fields: []string{"SeatRow.ShadowDrawn"}, Exempt: "GAP: shadow draw seed, pool, Beta parameters, sample and history rows count (SeatRow.ShadowDrawn records only that a draw happened); fixed in Task 8b"},
	"cmd/corral/shadow_draw.go:describeDraw":    {Fields: []string{"SeatRow.ShadowDrawn"}, Exempt: "GAP: shadow draw seed, pool, Beta parameters, sample and history rows count (SeatRow.ShadowDrawn records only that a draw happened); fixed in Task 8b"},
	"cmd/corral/shadow_draw.go:describeHistory": {Fields: []string{"SeatRow.ShadowDrawn"}, Exempt: "GAP: shadow draw seed, pool, Beta parameters, sample and history rows count (SeatRow.ShadowDrawn records only that a draw happened); fixed in Task 8b"},

	"cmd/corral/state.go:runStateMigrate":    {Exempt: "error message: byte sizes of two state files that both exist, no audit run"},
	"cmd/corral/tests_cmd.go:runTestsMap":    {Exempt: "error message: the rejected --workers value"},
	"cmd/corral/tests_cmd.go:printTestsMap":  {Exempt: "tests map verb: a read-only inventory that writes no ledger entry; the same unit counts are Task 4's UnitPass when a scan runs the evidence pass"},
	"cmd/corral/tests_cmd.go:listCapped":     {Exempt: exTruncation},
	"cmd/corral/timing_line.go:timingLine":   {Fields: []string{"Row.MutantsGraded", "Row.MutantMillisMedian", "Row.MutantMillisMax", "Row.DevPassMillis"}},
	"cmd/corral/timing_line.go:durationText": {Exempt: exFmtHelper},

	"cmd/corral/verify.go:runCertifyVerify":            {Fields: []string{"ScanRow.RekorLogIndex"}},
	"cmd/corral/verify_attest.go:verifySignature":      {Exempt: "error message: the Ed25519 public key length constant"},
	"cmd/corral/verify_attest.go:verifyWarehouseRows":  {Exempt: "error message: scan id and push count of a failed warehouse check"},
	"cmd/corral/verify_attest.go:verifyRekorInclusion": {Fields: []string{"ScanRow.RekorLogIndex"}},
	"cmd/corral/verify_attest.go:runVerifyLedger":      {Exempt: "verify result: entries checked and entries with a problem, computed at verify time"},
	"cmd/corral/verify_attest.go:verifyReviewEntry":    {Exempt: exReview},

	"internal/reposcan/cachekey.go:CacheKey":                  {Exempt: exDigestFrame},
	"internal/reposcan/cachekey.go:DigestTestSurface":         {Exempt: exDigestFrame},
	"internal/reposcan/cachekey.go:DigestDir":                 {Exempt: exDigestFrame},
	"internal/reposcan/tree_digest.go:TreeDigest":             {Exempt: exDigestFrame},
	"internal/reposcan/rank.go:Rank":                          {Exempt: "GAP: the count of candidates that could not be sized and were ranked last; the note is printed, no field carries it; fixed in Task 8b"},
	"internal/reposcan/selection.go:emptySelectionRunNote":    {Exempt: "error message: exit code of a selection evidence run that printed nothing"},
	"internal/reposcan/selection.go:CollectSelectionEvidence": {Exempt: "error message: exit code of an unparseable selection evidence run"},
	"internal/reposcan/unit_evidence.go:CollectUnitEvidence":  {Exempt: "error message: a bad worker count and the exit code of the failed unit listing"},

	// Functions the wider walk (any fmt print of a non-literal, %v, strconv, a
	// const format; every package a corral run executes) added. Most are error
	// messages and status text whose numbers are limits, ids and exit codes.
	"cmd/corral/brief.go:runBrief":                                       {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_change.go:extractTar":                            {Exempt: "error message: the tar entry type that cannot be materialized"},
	"cmd/corral/certify_change.go:runCertifyPubkey":                      {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_change.go:runCertifyStandalone":                  {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_local.go:authoredLandingFor":                     {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"cmd/corral/certify_local.go:herdNotConfiguredErr":                   {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_local.go:printLocalReadout":                      {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_local.go:renderModelSpend":                       {Fields: []string{"ModelCallRow.Calls", "ModelCallRow.InputTokens", "ModelCallRow.OutputTokens", "ModelCallRow.UnmeteredCalls", "ModelCallRow.CachedInputTokens"}},
	"cmd/corral/certify_local.go:writeLocalRecordFile":                   {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/certify_local_bugcatch.go:Record":                        {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_bugcatch.go:wireLocalBugCatch":             {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_criticscore.go:wireLocalCriticScore":       {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_entry.go:localSandboxIdentity":             {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_entry.go:writeLocalLedgerEntry":            {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_mutantattempts.go:Record":                  {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_mutantattempts.go:wireLocalMutantAttempts": {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_local_repo.go:buildRepoSeed":                     {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"cmd/corral/certify_local_repo.go:ensureVendored":                    {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/certify_local_repo.go:loadRepoFiles":                     {Exempt: "error message: the jail seed limit in MiB and the size of the repo text that exceeded it, a refusal before any run"},
	"cmd/corral/certify_local_tape.go:add":                               {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo.go:checkArgvSpansOneLanguage":               {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/certify_repo.go:collectSelection":                        {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo.go:goModulePath":                            {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"cmd/corral/certify_repo.go:groupThousands":                          {Exempt: "formatting helper: groups the digits of a number its caller passes in; the caller's row carries the field"},
	"cmd/corral/certify_repo.go:keepMachineLedgerCopy":                   {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo.go:machineLedgerOutsideCheckout":            {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo.go:resolveGoalSource":                       {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo.go:runPreflight":                            {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo.go:writeEvidenceCache":                      {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo_record.go:buildScanFileRows":                {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo_transparency.go:transparencyFailed":         {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/certify_repo_transparency.go:writeStatement":             {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"cmd/corral/control.go:runControl":                                   {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/critic_best_effort.go:openCriticStoreBestEffort":         {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/criticscore.go:runCriticScore":                           {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/demo.go:runDemo":                                         {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/doctor.go:checkPairing":                                  {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/doctor.go:checkSandbox":                                  {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/doctor.go:checkToolchain":                                {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/eval.go:RunOne":                                          {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/eval.go:runEval":                                         {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/ledger.go:runLedger":                                     {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/ledger.go:runLedgerAppend":                               {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/ledger.go:runLedgerRetract":                              {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/ledger_signer.go:rootGate":                               {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/ledger_signer.go:runLedgerSigner":                        {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/ledger_signer.go:runSignerAdd":                           {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/ledger_signer.go:runSignerList":                          {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/ledger_signer.go:runSignerRevoke":                        {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/main.go:main":                                            {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/main.go:printVersion":                                    {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/mcp_findings.go:runFindingsMCP":                          {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/models_rank.go:runModels":                                {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/models_rank.go:runModelsRank":                            {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/review.go:newWorktree":                                   {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"cmd/corral/review.go:runReview":                                     {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/review.go:runReviewAdjudicate":                           {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/review.go:runReviewShow":                                 {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/review_churn.go:gitBlameLine":                            {Exempt: "git argument: a line number handed to `git blame -L`, and an error message naming it"},
	"cmd/corral/review_churn.go:runReviewChurn":                          {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/run_bounds.go:resolveSeedLimit":                          {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/scans.go:runScansShowJSONWithTiming":                     {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/seal.go:openSealDB":                                      {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"cmd/corral/seal.go:runSeal":                                         {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/secret.go:runSecret":                                     {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/setup.go:pickAgents":                                     {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/setup.go:runSetupWith":                                   {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/shadow_draw.go:shadowCandidates":                         {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/signalctx.go:interruptContext":                           {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/state.go:printKeys":                                      {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/state.go:runState":                                       {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/tests_cmd.go:runTests":                                   {Exempt: "usage text and names printed via a string operand; no measured number"},
	"cmd/corral/ui.go:runUI":                                             {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/verdict_cache.go:newLedgerCache":                         {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/verify.go:httpPubkeyFetcher":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"cmd/corral/verify_attest.go:checkTelemetryDigest":                   {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"cmd/corral/verify_attest.go:runVerifyAttest":                        {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"cmd/corral/verify_attest.go:verifyTelemetry":                        {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/adequacy/fetch.go:readFetchStdout":                         {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/adequacy/jail.go:shareCache":                               {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/adequacy/pool.go:Probe":                                    {Fields: []string{"Row.Trees", "Row.ConcurrencyNote"}},
	"internal/adequacy/render_hunk.go:renderAnchoredHunk":                {Fields: []string{"MutantRow.SpanStart", "MutantRow.SpanEnd"}, Exempt: "prompt text for the test-writer: the line numbers of a survivor's hunk and a count of omitted unchanged lines"},
	"internal/adequacy/render_hunk.go:renderWholeFileDiff":               {Fields: []string{"MutantRow.SpanStart", "MutantRow.SpanEnd"}, Exempt: "prompt text for the test-writer: the line numbers of a survivor's hunk and a count of omitted unchanged lines"},
	"internal/adequacy/score.go:Score":                                   {Fields: []string{"Row.Detail"}, Exempt: "error message: durations and exit codes of a failed probe; the count of mutants the compile gate could not judge is printed only inside the Row.Detail note text"},
	"internal/advpool/aggregate.go:aggregate":                            {Fields: []string{"Row.MutantsDistinct", "Row.KillRate", "Row.Detail"}},
	"internal/advpool/budget.go:String":                                  {Fields: []string{"Row.MutantBudget", "Row.MutantBudgetRule"}},
	"internal/advpool/driver.go:NewDriver":                               {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/advpool/driver.go:StartRun":                                {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/advpool/driver.go:Tick":                                    {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/advpool/driver.go:checkNoProgress":                         {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/advpool/driver.go:criticTestCommand":                       {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/advpool/driver.go:progressFingerprint":                     {Exempt: "stall-detection fingerprint compared from tick to tick, never shown"},
	"internal/advpool/driver.go:tickDevAdequacy":                         {Fields: []string{"Row.MutantsInvalid"}, Exempt: "error message: retry counters of a shard reissue; shard key formatting"},
	"internal/advpool/driver.go:tickPoolAdequacy":                        {Exempt: "error message: retry counters of a test-writer reissue"},
	"internal/advpool/driver_writer_fanout.go:advanceWriterAttempt":      {Exempt: "error message: repair counters of a per-survivor writer reissue"},
	"internal/advpool/gate.go:Error":                                     {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/advpool/gate.go:SignVerdict":                               {Fields: []string{"SeatRow.ShadowDrawn"}, Exempt: "GAP: shadow draw pool size (SeatRow.ShadowDrawn records only that a draw happened); fixed in Task 8b"},
	"internal/advpool/gate.go:authoredTestPath":                          {Exempt: "file name suffix for an authored test, an index not a measurement"},
	"internal/advpool/gate.go:selectionDisclosure":                       {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/advpool/gate.go:selectionRowErr":                           {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/advpool/roles.go:ShadowShardTaskKey":                       {Exempt: "task key formatting from a shard index"},
	"internal/advpool/roles.go:ShardTaskKey":                             {Exempt: "task key formatting from a shard index"},
	"internal/advpool/roles.go:shardCode":                                {Fields: []string{"Row.Symbols"}, Exempt: "prompt text for the mutant generator: a count of elided source lines"},
	"internal/advpool/terminal.go:Error":                                 {Fields: []string{"Row.RegionsTotal", "Row.DroppedRegions"}},
	"internal/agentbackend/agentbackend.go:Chat":                         {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/agentbackend/agentbackend.go:agentSecret":                  {Exempt: "diagnostic text on stderr: causes, paths and names via %v or a string operand; no measured number"},
	"internal/agentbackend/agentseat.go:AgentBinary":                     {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/agentbackend/agentseat.go:Chat":                            {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/agentbackend/agentseat.go:newAgentSeat":                    {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/agentbackend/agentwire.go:args":                            {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/agentbackend/agentwire.go:codexErrorText":                  {Exempt: "error message: an HTTP status code in a failure text"},
	"internal/agentbackend/agentwire.go:parse":                           {Exempt: "error message: an HTTP status code in a failure text"},
	"internal/agentsetup/detect.go:detectOne":                            {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/agentsetup/detect.go:fromFile":                             {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/agentsetup/detect.go:interpret":                            {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/agentsetup/plan.go:Render":                                 {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/agentworker/agentworker.go:runCriticLoop":                  {Fields: []string{"Row.CriticIncomplete", "Row.VacuousFindings"}},
	"internal/agentworker/critic_typed.go:RunCriticTyped":                {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/agentworker/critic_typed.go:batchLabel":                    {Exempt: "progress label: a batch index of a typed-critic run"},
	"internal/agentworker/critic_typed.go:batchSummary":                  {Fields: []string{"Row.VacuousFindings", "Row.SuiteTests"}},
	"internal/agentworker/critic_typed.go:criticTypedOnce":               {Fields: []string{"Row.VacuousFindings", "Row.SuiteTests"}},
	"internal/agentworker/critic_typed.go:parseTypedVerdicts":            {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/annindex/annindex.go:migrateToArray":                       {Exempt: "SQL type literal built from the vector dimension"},
	"internal/attest/attest.go:Verify":                                   {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/attest/attest.go:canonical":                                {Exempt: "canonical signing input: timestamps formatted into the bytes that are signed"},
	"internal/auditpush/bundle.go:attachWithAutoCreate":                  {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/auditpush/bundle.go:insertBundle":                          {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/auditpush/bundle.go:requireStatements":                     {Exempt: "error message: names a scan row or event by repo, scan id and sequence"},
	"internal/auditpush/ledgerdir.go:WriteCheckpoint":                    {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/auditpush/pubkey.go:parseLedgerPubKey":                     {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/auditpush/push.go:Push":                                    {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/advpool.go:Record":                                   {Exempt: "formats a record id and finding id into a key string, not a measurement"},
	"internal/brain/advpool.go:StartRun":                                 {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/askfleet.go:registerAskFleet":                        {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/browser.go:registerBrowser":                          {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/fleetclaims.go:registerFleetClaims":                  {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/health.go:DetectRoleStalls":                          {Exempt: "brain health diagnostic: how long a ready task has waited for an agent; a coordination message, not an audit result"},
	"internal/brain/herd.go:ResolveHerdInputs":                           {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/learn.go:ApproveProposal":                            {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/learn.go:RejectProposal":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/missions.go:registerMissions":                        {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/recordings.go:registerRecordings":                    {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/reposync.go:registerRepoSync":                        {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/seeddocs.go:readCapped":                              {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/subagents.go:budgetDecision":                         {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/brain/task_artifacts.go:registerTaskArtifacts":             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/buildstore/store.go:List":                                  {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/buildstore/store.go:LoadOrCreateSigningKey":                {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/certify/certify.go:VerifyLedger":                           {Exempt: "hash framing: sequence number formatted into the bytes hashed"},
	"internal/certify/signbuild.go:SignBuild":                            {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/controlgate/post.go:describeResult":                        {Exempt: "control gate result for the brain's control stack, a separate record from the scan ledger"},
	"internal/coord/inbox.go:RecentInstructions":                         {Exempt: "SQL LIMIT value from a caller's argument"},
	"internal/creds/creds.go:Redact":                                     {Exempt: "length of a secret shown redacted, a safety label"},
	"internal/embed/embed.go:Embed":                                      {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/embed/embed.go:VecLiteral":                                 {Exempt: "vector literal serialization"},
	"internal/eval/corpus.go:Load":                                       {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/eval/harness.go:Run":                                       {Exempt: "eval harness progress: targets, iterations and runs of its own calibration corpus; the harness is a calibration tool, not a scan, and writes no ledger entry"},
	"internal/eval/progress.go:progKey":                                  {Exempt: "progress file key: corpus version, target id and iteration"},
	"internal/eval/report.go:Report":                                     {Exempt: "eval harness calibration verdict over its own corpus runs, not a scan; writes no ledger entry"},
	"internal/eval/report.go:WriteReportWithScope":                       {Exempt: "eval harness calibration table over its own corpus runs, not a scan; writes no ledger entry"},
	"internal/fleet/sync.go:Sync":                                        {Exempt: "sync cursor formatted into a query"},
	"internal/gate/config.go:ParsePolicy":                                {Exempt: "policy text naming the maximum gate timeout, a limit constant"},
	"internal/lang/concat.go:idSuffix":                                   {Exempt: "identifier suffix: a hex code point in a renamed test identifier"},
	"internal/lang/concat.go:mergeParts":                                 {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/lang/corralcov.go:Error":                                   {Exempt: "error message: how many files the coverage reducer disclosed it could not count"},
	"internal/lang/corralcov.go:Summary":                                 {Exempt: "error summary: how many files the coverage reducer disclosed it could not count"},
	"internal/lang/corralcov.go:corralReportBody":                        {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/lang/corralcov.go:corralReportKept":                        {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/lang/go.go:TreeEnv":                                        {Exempt: "environment value for a child process: the core count a tree was given"},
	"internal/lang/go.go:parseGoProfile":                                 {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/lang/php.go:phpJailPreflight":                              {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/lang/python.go:CoverageCounts":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/lang/python.go:Preflight":                                  {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/lang/python.go:pyPreflightStockDefault":                    {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/lang/rust_landing.go:rustFreeModuleName":                   {Exempt: "module name suffix from a counter"},
	"internal/lang/rust_workspace.go:owningCrateDir":                     {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/learn/draft.go:Draft":                                      {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/ledger/derive.go:ledgerProblem":                            {Exempt: "verify-time problem text: how many entries are off the single chain, computed when the ledger is read"},
	"internal/ledger/keyless.go:KeylessArtifact":                         {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/ledger/keyless.go:UnmarshalJSON":                           {Exempt: "decoding helper: integer formatted for parsing"},
	"internal/ledger/keyless.go:verifyBundle":                            {Exempt: "error message and the canonical transparency-log body whose index and time are the Sigstore bundle's own"},
	"internal/ledger/keyless.go:verifyCheckpoint":                        {Exempt: "error message: tree size mismatch in a checkpoint proof"},
	"internal/ledger/keyless.go:verifyInclusion":                         {Exempt: "error message: inclusion proof sizes"},
	"internal/ledger/scanuid.go:ScanUID":                                 {Exempt: "identity framing: the scan id and length-prefixed fields hashed into a scan uid"},
	"internal/ledger/sigstoreroot.go:ParseSigstoreRoot":                  {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/ledger/strictjson.go:walkJSON":                             {Exempt: "JSON path formatting for an error message"},
	"internal/ledger/verify.go:walk":                                     {Fields: []string{"Checkpoint.Entries"}, Exempt: "review metrics: phase 1b"},
	"internal/memory/store_cgo.go:searchHNSWFiltered":                    {Exempt: "SQL parameter: vector dimension"},
	"internal/memory/store_nocgo.go:serializeVector":                     {Exempt: "vector literal serialization"},
	"internal/mission/engine.go:progressFingerprint":                     {Exempt: "stall-detection fingerprint compared from tick to tick, never shown"},
	"internal/mission/routing.go:Judge":                                  {Exempt: "brain staffing prompt: host cores, RAM and VRAM offered to a routing model; the brain's mission record, not a scan"},
	"internal/mission/routing.go:Staff":                                  {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/mission/routing.go:buildLeaderboardBrief":                  {Exempt: "brain staffing prompt: a model's task counts and pass rate from the brain's leaderboard, not a scan"},
	"internal/mission/routing.go:staffOnce":                              {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/mission/store.go:CancelMission":                            {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/mission/store.go:MissionDir":                               {Exempt: "directory name from a mission id"},
	"internal/mission/store.go:PauseMission":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/mission/store.go:ResolveNeedsReview":                       {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/mission/store.go:ResumeMission":                            {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/mission/store.go:planToTasks":                              {Exempt: "task key formatting from a plan index"},
	"internal/modelcorr/modelcorr.go:Compare":                            {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/modelrank/rank.go:prefer":                                  {Exempt: "rank note: the operator's --min-runs threshold echoed back"},
	"internal/modelrank/rank.go:rowFor":                                  {Fields: []string{"SeatRow.Catches", "SeatRow.Opportunities", "Row.MutantsPlanted", "Row.MutantsGraded", "Row.MutantsInvalid", "Row.Survivors"}, Exempt: "review metrics: phase 1b"},
	"internal/models/models.go:Load":                                     {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/models/models.go:Parse":                                    {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/ollamareq/ollamareq.go:ContextOverflowHint":                {Exempt: "error hint: the num_ctx the request asked Ollama for"},
	"internal/oracle/oracle.go:narrate":                                  {Exempt: "display truncation count of a printed list"},
	"internal/oracle/sandbox.go:runLocked":                               {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/prior/prior.go:Digest":                                     {Exempt: "digest framing of a tried-edit list, not a measurement"},
	"internal/prior/prior.go:Render":                                     {Fields: []string{"Row.PriorsApplied", "MutantRow.SpanStart", "MutantRow.SpanEnd"}},
	"internal/prior/prior.go:mergeTried":                                 {Exempt: "merge key from a span, not a measurement"},
	"internal/prior/prior.go:spanOf":                                     {Fields: []string{"MutantRow.SpanStart", "MutantRow.SpanEnd"}},
	"internal/queue/supersede.go:CancelTaskGuarded":                      {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/queue/supersede.go:RetargetDependents":                     {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/queue/supersede.go:uniqueKeyTx":                            {Exempt: "task key suffix from a counter"},
	"internal/recordings/store.go:Query":                                 {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/recordview/pages.go:Paths":                                 {Exempt: "page file name from a page index"},
	"internal/recordview/pages.go:funcs":                                 {Exempt: "formatting helper: abbreviates a token count the page already holds"},
	"internal/recordview/pages.go:listPath":                              {Exempt: "page file name from a page index"},
	"internal/recordview/pages.go:neighbours":                            {Exempt: "page file name from a page index"},
	"internal/recordview/scan.go:buildScanView":                          {Fields: []string{"Row.KillRate", "Row.MutantsDistinct", "Row.MutantsGraded"}},
	"internal/recordview/view.go:outcomeOf":                              {Fields: []string{"Finding.ExitCode"}, Exempt: "review metrics: phase 1b"},
	"internal/reference/ingest.go:FetchText":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/reference/ingest.go:Ingest":                                {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/reference/store.go:searchHNSW":                             {Exempt: "SQL parameter: vector dimension"},
	"internal/repo/provider.go:rcGetPR":                                  {Exempt: "URL path of a hosting-provider API call, including a pull request number"},
	"internal/repo/provider.go:rcListReviewComments":                     {Exempt: "URL path of a hosting-provider API call, including a pull request number"},
	"internal/repo/provider.go:rcListReviews":                            {Exempt: "URL path of a hosting-provider API call, including a pull request number"},
	"internal/repo/provider.go:rcPostComment":                            {Exempt: "URL path of a hosting-provider API call, including a pull request number"},
	"internal/repo/provider_gitlab.go:GetPR":                             {Exempt: "URL path of a hosting-provider API call, including a merge request number"},
	"internal/repo/provider_gitlab.go:ListReviewComments":                {Exempt: "URL path of a hosting-provider API call, including a merge request number"},
	"internal/repo/provider_gitlab.go:ListReviews":                       {Exempt: "URL path of a hosting-provider API call, including a merge request number"},
	"internal/repo/provider_gitlab.go:PostComment":                       {Exempt: "URL path of a hosting-provider API call, including a merge request number"},
	"internal/repo/read.go:Grep":                                         {Exempt: "code-search result handed to an agent: a line number in a file"},
	"internal/repo/repo.go:CheckoutPR":                                   {Exempt: "git ref name from a pull request number"},
	"internal/repo/repo.go:RepoIdent":                                    {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/repo/repo.go:git":                                          {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/repo/snapshot.go:Snapshot":                                 {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/repoindex/search.go:mergeHits":                             {Exempt: "dedupe key from a start line"},
	"internal/repoindex/store_nocgo.go:serializeVector":                  {Exempt: "vector literal serialization"},
	"internal/reposcan/baseline.go:CheckBaselineStable":                  {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/reposcan/derive.go:GoalFor":                                {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/reposcan/preflight.go:Preflight":                           {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/reposcan/preflight.go:PreflightCommand":                    {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/reposcan/selection.go:SelectionCommand":                    {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/reposcan/selection.go:selectionRow":                        {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/reposcan/unit_evidence.go:runUnit":                         {Exempt: "the exit code of a failed unit is in its failure note, reported in the run's output; the count of failed units is UnitPass.failed"},
	"internal/reposcan/unit_map.go:NoRunnerRow":                          {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/review/agent.go:AgentBrief":                                {Exempt: "review metrics: phase 1b"},
	"internal/review/brief.go:Brief":                                     {Exempt: "review metrics: phase 1b"},
	"internal/review/review.go:CoverageNote":                             {Exempt: "review metrics: phase 1b"},
	"internal/review/review.go:Parse":                                    {Exempt: "review metrics: phase 1b"},
	"internal/review/review.go:Reproduce":                                {Exempt: "review metrics: phase 1b"},
	"internal/review/verifier.go:VerifierAgentBrief":                     {Exempt: "review metrics: phase 1b"},
	"internal/review/verifier.go:VerifierBrief":                          {Exempt: "review metrics: phase 1b"},
	"internal/review/verifier.go:Verify":                                 {Exempt: "review metrics: phase 1b"},
	"internal/sandbox/identity.go:Identify":                              {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/sandbox/isolator_darwin.go:Preflight":                      {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/sandbox/isolator_linux.go:Preflight":                       {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/shadowpool/shadowpool.go:FormatSeed":                       {Exempt: "GAP: shadow draw seed, which --shadow-seed replays; fixed in Task 8b"},
	"internal/statedir/legacy.go:LegacyDir":                              {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/statedir/migrate.go:ownedByMe":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/statedir/statedir.go:noRoot":                               {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/testgen/parse_diag.go:Error":                               {Exempt: "error message: counts of malformed, no-op and unmatched blocks in a generator reply that produced nothing usable"},
	"internal/testgen/parse_diag.go:parseMutantsDiag":                    {Exempt: "mutant id from a counter"},
	"internal/testgen/testgen.go:mutantFormatInstruction":                {Fields: []string{"Row.MutantBudget"}, Exempt: "prompt text for the mutant generator"},
	"internal/testmap/json.go:Digest":                                    {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/testmap/json.go:Read":                                      {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/testmap/select.go:AuthoredTestCmd":                         {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/testmap/select.go:selectWithout":                           {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/testmap/select.go:selectedIDs":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/testmap/select.go:someUnitReported":                        {Exempt: "error message: how many units were listed and none reported"},
	"internal/testmap/select.go:unitsCmd":                                {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/testmap/select.go:widenByNode":                             {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/transparency/logger.go:Get":                                {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/transparency/logger.go:logEntryFromResponse":               {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/transparency/rekor.go:VerifyInclusion":                     {Exempt: "status text: names, paths, hashes and passthrough strings printed via %v or a string operand; no measured number"},
	"internal/transparency/rekor.go:parseEnvelope":                       {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/transparency/rekor.go:parseInclusionProof":                 {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
	"internal/transparency/rekor.go:soleEntry":                           {Exempt: "error message: the numbers are limits, ids, exit codes, offsets and counts inside a failure text, not an audit measurement"},
	"internal/transparency/rekor.go:toEntry":                             {Exempt: "error message: builds failure text from causes, ids and paths via %v; no measured number"},
}
