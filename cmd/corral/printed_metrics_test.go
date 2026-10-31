// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
//   - "review metrics: phase 1b": plan ruling R5, review's printers.
//
// Every other Exempt names why the number is not a measurement of a run:
// usage text, an error message, a framing length, a key.
type printedRow struct {
	Fields []string
	Exempt string
}

var numericVerb = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[dfgeGx]`)

// printingFuncs returns "<package dir>/<file>:<func>" for every non-test
// function that formats a number with fmt.Fprintf, Printf or Sprintf.
func printingFuncs(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	for _, dir := range []string{".", "../../internal/reposcan"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Base(dir)
		if dir == "." {
			pkg = "cmd/corral"
		} else {
			pkg = "internal/reposcan"
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f) // #nosec G304 -- walks this repo's own sources
			if err != nil {
				t.Fatal(err)
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range af.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				hit := false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "fmt" {
						return true
					}
					idx := 0
					switch sel.Sel.Name {
					case "Fprintf":
						idx = 1
					case "Printf", "Sprintf":
					default:
						return true
					}
					if len(call.Args) <= idx {
						return true
					}
					lit, ok := call.Args[idx].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						return true
					}
					if numericVerb.MatchString(strings.ReplaceAll(s, "%%", "")) {
						hit = true
					}
					return true
				})
				if hit {
					found[pkg+"/"+filepath.Base(f)+":"+fd.Name.Name] = true
				}
			}
		}
	}
	return found
}

// TestEveryPrintedNumberIsRecordedOrExempt is the gate behind AGENTS.md's "a
// real measurement, correctly computed, then dropped on the floor": a function
// that prints a number must name the ledger fields that carry it, or say why
// no field should. A new printing function fails here until someone decides.
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
		Exempt: "unmeasured-mutant, duplicate-mutant and ungraded-writer-seat counts live in the verdict document (Row.VerdictJSON), not a typed field"},

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
		Exempt: gap("mutant scoring concurrency and per-file worker count", "Task 5") + "; UNCOVERED: the excluded-file count of the walk line, no ledger field holds exclusions by reason; counts in the 'ledger: entry written' and 'pushed' lines repeat the rows the entry holds"},
	"cmd/corral/certify_repo.go:goalCacheDisclosureLine": {Fields: []string{"Row.GoalsDerived", "Row.GoalReused"}},
	"cmd/corral/certify_repo.go:pickPreflightLanguage":   {Fields: []string{"ScanRow.PreflightNote"}},
	"cmd/corral/certify_repo.go:selectionCmdDigest":      {Exempt: exDigestFrame},
	"cmd/corral/certify_repo.go:noEvidenceSourcesNote":   {Exempt: "UNCOVERED: per-language count of sources whose test landing is not built; shown as a selection note, recorded only as free text if at all"},
	"cmd/corral/certify_repo.go:printPreflightReport": {Fields: []string{"ScanRow.PreflightRan", "ScanRow.PreflightNote", "Row.Uncovered"},
		Exempt: "UNCOVERED: the counts of files executed at least once and files not measured by the coverage preflight"},
	"cmd/corral/certify_repo.go:candidacySummaryLine":   {Fields: []string{"Row.Evidence", "Row.ImportOnly", "Row.Uncovered", "Row.Disposition"}},
	"cmd/corral/certify_repo.go:printEvidencePairings":  {Fields: []string{"Row.CoveringUnits", "Row.CoveringTests"}, Exempt: exTruncation},
	"cmd/corral/certify_repo.go:printSearchPairings":    {Exempt: exTruncation},
	"cmd/corral/certify_repo.go:printWouldAudit":        {Exempt: "dry-run inventory count, no run happens"},
	"cmd/corral/certify_repo.go:printExclusions":        {Exempt: "UNCOVERED: excluded-file counts by exclusion reason; the ledger holds audited files and totals, not exclusions by reason"},
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
	"cmd/corral/certify_repo.go:redUnitsPhrase":        {Exempt: gap("units that ran red in the evidence pass", "Task 4")},
	"cmd/corral/certify_repo.go:printLanguageProfile":  {Exempt: "UNCOVERED: per-language counts of auditable, unpaired and ambiguous source files; the ledger holds audited files, not the language inventory"},
	"cmd/corral/certify_repo.go:writerModeDisclosure": {Fields: []string{"Row.WriterMode", "ModelCallRow.Calls"},
		Exempt: "UNCOVERED: the writer attempts-per-survivor spread (min, median, max) and the count of ungraded writer seats"},
	"cmd/corral/certify_repo.go:examConfidenceLine": {Fields: []string{
		"Row.KillRate", "Row.MutantsDistinct", "Row.SymbolsProbed", "Row.Symbols", "Row.DecisionsProbed", "Row.Decisions"},
		Exempt: "UNCOVERED: the count of mutants the exam could not place (ExamCoverage.Unplaced)"},
	"cmd/corral/certify_repo.go:writerPairLine": {Fields: []string{
		"Row.ChallengerMutants", "Row.ChallengerSurvivedWriter", "Row.ChallengerSurvivedShadow", "Row.ChallengerShared",
		"Row.ChallengerUnion", "Row.ChallengerJaccard"}},
	"cmd/corral/certify_repo_transparency.go:uploadToTransparencyLog": {Fields: []string{"ScanRow.RekorLogIndex"}},
	"cmd/corral/certify_repo_units.go:collectUnits":                   {Exempt: gap("units listed and run, and the unit worker count and evidence timeout", "Task 4 (counts) and Task 5 (workers, timeout)")},
	"cmd/corral/certify_repo_units.go:unitsCmdDigest":                 {Exempt: exDigestFrame},
	"cmd/corral/certify_repo_units.go:printUnitPass":                  {Exempt: gap("units run, failed, green as written, unreported and the worker count", "Task 4")},
	"cmd/corral/certify_repo_units.go:printUnitGrading":               {Exempt: gap("units that reported evidence and units that did not", "Task 4")},

	"cmd/corral/cost_line.go:costLine":         {Fields: []string{"ModelCallRow.Calls", "ModelCallRow.InputTokens", "ModelCallRow.OutputTokens", "ModelCallRow.UnmeteredCalls", "ModelCallRow.CachedInputTokens"}},
	"cmd/corral/cost_line.go:abbreviateTokens": {Exempt: exFmtHelper},
	"cmd/corral/cost_line.go:budgetLine": {Fields: []string{"ScanRow.ModelCalls", "ScanRow.InputTokens", "ScanRow.OutputTokens"},
		Exempt: gap("the --max-tokens cap and whether it was reached", "Task 5")},

	"cmd/corral/doctor.go:runDoctor":       {Exempt: "doctor result: how many checks failed, no audit run to record into"},
	"cmd/corral/doctor.go:probeDeriveSeat": {Exempt: "UNCOVERED: doctor's probe spends one model call and prints its tokens; doctor writes no scan, so nothing carries the spend"},

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

	"cmd/corral/run_bounds.go:evidenceCutNote": {Exempt: gap("units cut short by the evidence bound", "Task 4")},

	"cmd/corral/scans.go:runScansList":          {Fields: []string{"ScanRow.Audited", "ScanRow.Candidates", "Row.CacheHit", "Row.KillRate"}},
	"cmd/corral/scans.go:runScansShow":          {Fields: []string{"Row.Survivors", "Row.ProvenMissed", "Row.KillRate", "ScanRow.RekorLogIndex"}},
	"cmd/corral/scans.go:authoredTestOutcome":   {Fields: []string{"Row.Survivors"}},
	"cmd/corral/scans.go:scanFileSelectionWith": {Fields: []string{"Row.TestSelection", "Row.SelectedTests", "Row.SuiteTests", "Row.SelectionUnreported", "TestsPerMutantSpread.Min", "TestsPerMutantSpread.Max"}},
	"cmd/corral/scans.go:excludedSuffix":        {Fields: []string{"Row.SelectionExcluded"}},
	"cmd/corral/scans.go:formatKillRate":        {Fields: []string{"Row.KillRate"}},
	"cmd/corral/scans.go:denominatorNote":       {Fields: []string{"Row.MutantsGraded", "Row.MutantsInvalid", "Row.DroppedRegions"}},
	"cmd/corral/scans.go:scanFileBudget":        {Fields: []string{"Row.MutantBudget", "Row.MutantBudgetRule"}},
	"cmd/corral/scans_ledger.go:EntryKind":      {Fields: []string{"Checkpoint.Entries"}, Exempt: exReview},

	"cmd/corral/scorecard.go:runScorecard":  {Fields: []string{"SeatRow.Catches", "SeatRow.Opportunities"}},
	"cmd/corral/scorecard.go:pctOrDash":     {Exempt: exFmtHelper},
	"cmd/corral/seal.go:runSealWithoutRepo": {Fields: []string{"Row.KillRate", "Row.Survivors", "Row.ProvenMissed", "Row.Trees"}},
	"cmd/corral/seal.go:runSealWithRepo":    {Fields: []string{"Row.KillRate", "Row.Survivors", "Row.ProvenMissed", "Row.Trees", "Row.Disposition", "Row.ImportOnly", "Row.Uncovered"}},
	"cmd/corral/seal.go:trimPercent":        {Exempt: exFmtHelper},
	"cmd/corral/seedskip.go:seedSkipsLine":  {Fields: []string{"ScanRow.SeedSkipped"}},
	"cmd/corral/setup.go:renderPickList":    {Exempt: "menu index of an interactive pick list, not a measurement"},

	"cmd/corral/shadow_draw.go:drawShadowSeats": {Fields: []string{"SeatRow.ShadowDrawn"}},
	"cmd/corral/shadow_draw.go:describeDraw":    {Fields: []string{"SeatRow.ShadowDrawn"}},
	"cmd/corral/shadow_draw.go:describeHistory": {Fields: []string{"SeatRow.ShadowDrawn"}},

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
	"internal/reposcan/rank.go:Rank":                          {Exempt: "UNCOVERED: the count of candidates that could not be sized and were ranked last; the note is printed, no field carries it"},
	"internal/reposcan/selection.go:emptySelectionRunNote":    {Exempt: "error message: exit code of a selection evidence run that printed nothing"},
	"internal/reposcan/selection.go:CollectSelectionEvidence": {Exempt: "error message: exit code of an unparseable selection evidence run"},
	"internal/reposcan/unit_evidence.go:CollectUnitEvidence":  {Exempt: "error message: a bad worker count and the exit code of the failed unit listing"},
}
