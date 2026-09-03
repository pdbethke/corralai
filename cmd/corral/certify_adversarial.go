// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/brainclient"
	"github.com/pdbethke/corralai/internal/lang"
)

// advFinding is the subset of a queue.Finding the verdict render shows. The
// tags match queue.Finding's own (lowercase) wire tags.
type advFinding struct {
	Type          string `json:"type"`
	Severity      string `json:"severity"`
	Target        string `json:"target"`
	Evidence      string `json:"evidence"`
	ReporterModel string `json:"reporter_model"`
}

// advVerdict mirrors advpool.Verdict on the wire. advpool.Verdict has NO json
// tags, so its keys are the Go-default CAPITALIZED field names — matched here
// verbatim. Changing these breaks decoding.
type advVerdict struct {
	Repo           string   `json:"Repo"`
	Commit         string   `json:"Commit"`
	Lang           string   `json:"Lang"`
	DevKillRate    float64  `json:"DevKillRate"`
	MutantsTotal   int      `json:"MutantsTotal"`
	MutantsInvalid int      `json:"MutantsInvalid"`
	Survivors      int      `json:"Survivors"`
	ProvenMissed   int      `json:"ProvenMissed"`
	RegionsTotal   int      `json:"RegionsTotal"`
	RegionsProbed  int      `json:"RegionsProbed"`
	DroppedRegions []string `json:"DroppedRegions"`
	// DuplicateMutants: generated mutants that were byte-identical edits of
	// another and were collapsed before scoring — see
	// adequacy.DedupeMutants. Disclosed so the graded denominator is
	// explainable.
	DuplicateMutants int               `json:"duplicate_mutants,omitempty"`
	VacuousFindings  []advFinding      `json:"VacuousFindings"`
	ModelsByRole     map[string]string `json:"ModelsByRole"`
	Status           string            `json:"Status"`
	RecordID         int64             `json:"RecordID"`
	RecordHead       string            `json:"RecordHead"`
	// TestWriterFailed mirrors advpool.Verdict.TestWriterFailed: true when the
	// pool exhausted its compile-retry budget without authoring a compiling
	// killing test. See renderAdvVerdict for the honest readout this drives.
	TestWriterFailed bool `json:"TestWriterFailed"`
	// BaselineOutput mirrors advpool.Verdict.BaselineOutput: the failing
	// baseline's own output, so a COULD-NOT-GRADE readout can say WHY rather
	// than only THAT. See renderAdvVerdict.
	BaselineOutput string `json:"BaselineOutput"`
	// AuthoredTestNotCollected mirrors advpool.Verdict: the run PROVED the
	// test command never reached the authored test's file. Narrows
	// PoolTestUnsound to the half the operator can fix in one edit.
	AuthoredTestNotCollected bool `json:"AuthoredTestNotCollected"`
	// PoolTestUnsound mirrors advpool.Verdict.PoolTestUnsound: true when the
	// pool's authored test DID compile (TestWriterFailed is false) but its
	// own scoring report never genuinely graded (it failed on the unmutated
	// compliant code, the canary was never killed, or nothing was scored).
	// A DIFFERENT diagnosis from TestWriterFailed with the same honesty
	// consequence — see renderAdvVerdict for the readout this drives.
	PoolTestUnsound bool `json:"PoolTestUnsound"`
	// BaselineFailed mirrors advpool.Verdict.BaselineFailed: the dev suite did
	// not pass on the unmutated code in the jail, so nothing was graded. Drives
	// the "could not grade" readout instead of a fabricated kill tally.
	BaselineFailed bool `json:"BaselineFailed"`
	// SuiteIgnoresFile mirrors advpool.Verdict.SuiteIgnoresFile: the suite
	// passed on deliberately invalid source, so it never compiles or imports
	// the audited file and nothing about it was graded. A DIFFERENT readout
	// from BaselineFailed — the suite is fine, the check command is not
	// pointed at this file.
	SuiteIgnoresFile bool `json:"SuiteIgnoresFile"`
	// TimedOut mirrors advpool.Verdict.TimedOut: true when this verdict was
	// signed by a run that hit its wall-clock deadline before the pool
	// converged (RunDeadline — advpool/driver.go's Tick, or the --local
	// drive loop's own bankableTimeoutVerdict) rather than a clean run.
	// ProvenMissed and VacuousFindings are NEVER trustworthy on a TimedOut
	// verdict: the test-writer and test-critic never ran. Whether
	// DevKillRate/Survivors/MutantsTotal are real measurements or zero
	// values nothing computed depends ENTIRELY on DevScored, below — do not
	// assume TimedOut alone means the dev suite was measured. See
	// renderAdvVerdict for the readout this drives.
	TimedOut bool `json:"TimedOut"`
	// WriterProviderFailed mirrors advpool.Verdict.WriterProviderFailed:
	// the writer's provider never answered, so TestWriterFailed above is
	// not the model's doing.
	WriterProviderFailed bool `json:"writer_provider_failed,omitempty"`
	// DevScored mirrors advpool.Verdict.DevScored: true once the dev
	// suite's OWN kill-rate was actually measured against real mutants in
	// the real jail. On a TimedOut verdict this is the ONLY thing that
	// makes DevKillRate/Survivors/MutantsTotal real numbers rather than
	// zero values nothing ever computed — a run that stalls before the
	// mutant-generator itself finishes (reachable on the brain path via
	// advpool.Driver.Tick's own RunDeadline branch, not just --local's)
	// signs a TimedOut verdict with DevScored=false and a fabricated-
	// looking 0.00. renderAdvVerdict must treat TimedOut && !DevScored as
	// COULD-NOT-GRADE, never print the zero as a measurement.
	DevScored bool `json:"DevScored"`
	// PoolScored mirrors advpool.Verdict.PoolScored: true once the pool's
	// half was actually measured. It is what lets renderAdvVerdict tell a
	// timeout that PROVED survivors catchable (and then stalled on the
	// critic) from one that timed out before the writer ever ran — the
	// difference between reporting an execution-proven gap and discarding it.
	PoolScored bool `json:"PoolScored"`
	// WriterSeatsUngraded mirrors advpool.Verdict.WriterSeatsUngraded: how
	// many of a per-survivor run's writer seats never produced a test that
	// genuinely graded.
	//
	// Carried because ProvenMissed is not readable without it. The field's own
	// doc puts it plainly — "three seats unattempted changes what a 5 means" —
	// and the two flags beside it cannot express partial failure:
	// TestWriterFailed means NOTHING compiled anywhere, PoolTestUnsound means
	// nothing graded anywhere, so a file where 21 of 24 seats graded carries
	// neither. --repo has shown this since the fan-out landed; --local dropped
	// it at the converter and printed the bare count.
	WriterSeatsUngraded int `json:"writer_seats_ungraded,omitempty"`
}

// advStatus mirrors brain.AdvPoolStatusOut (get_adversarial_run's output).
type advStatus struct {
	RunID        int64       `json:"run_id"`
	Found        bool        `json:"found"`
	Converged    bool        `json:"converged"`
	Verdict      *advVerdict `json:"verdict"`
	AuthoredTest string      `json:"authored_test"`
}

// advStartSpec mirrors brain.AdvPoolRunSpec (start_adversarial_run's input).
type advStartSpec struct {
	Repo        string `json:"repo"`
	Commit      string `json:"commit"`
	Goal        string `json:"goal"`
	CodePath    string `json:"code_path"`
	Code        string `json:"code"`
	DevTestPath string `json:"dev_test_path"`
	DevTestCode string `json:"dev_test_code"`
	TestCmd     string `json:"test_cmd"`
	NMutants    int    `json:"n_mutants,omitempty"`
	Lang        string `json:"lang"`
}

// advPoolClient triggers and polls an adversarial-pool run over the brain's
// MCP tools. Injected so runCertifyAdversarial is testable without a brain.
type advPoolClient interface {
	StartRun(ctx context.Context, brainURL string, spec advStartSpec) (runID int64, err error)
	RunStatus(ctx context.Context, brainURL string, runID int64) (advStatus, error)
}

// mcpAdvClient is advPoolClient backed by real MCP calls, dialing the brain
// fresh per call with a token from the keystore (mirrors mcpPoster).
type mcpAdvClient struct{}

func (mcpAdvClient) call(ctx context.Context, brainURL, tool string, args map[string]any) (string, error) {
	token, err := brainToken()
	if err != nil {
		return "", fmt.Errorf("resolve brain token: %w", err)
	}
	cl, err := brainclient.Dial(ctx, brainURL, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = cl.Close() }()
	res, err := cl.CallTool(ctx, tool, args)
	if err != nil {
		return "", err
	}
	text := brainclient.FirstText(res)
	if res.IsError {
		msg := text
		if msg == "" {
			msg = tool + " reported an error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return text, nil
}

func (c mcpAdvClient) StartRun(ctx context.Context, brainURL string, spec advStartSpec) (int64, error) {
	args := map[string]any{
		"repo": spec.Repo, "commit": spec.Commit, "goal": spec.Goal,
		"code_path": spec.CodePath, "code": spec.Code,
		"dev_test_path": spec.DevTestPath, "dev_test_code": spec.DevTestCode,
		"test_cmd": spec.TestCmd, "lang": spec.Lang,
	}
	if spec.NMutants > 0 {
		args["n_mutants"] = spec.NMutants
	}
	text, err := c.call(ctx, brainURL, "start_adversarial_run", args)
	if err != nil {
		return 0, err
	}
	var out struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return 0, fmt.Errorf("decoding start_adversarial_run response: %w", err)
	}
	return out.RunID, nil
}

func (c mcpAdvClient) RunStatus(ctx context.Context, brainURL string, runID int64) (advStatus, error) {
	text, err := c.call(ctx, brainURL, "get_adversarial_run", map[string]any{"run_id": runID})
	if err != nil {
		return advStatus{}, err
	}
	var st advStatus
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		return advStatus{}, fmt.Errorf("decoding get_adversarial_run response: %w", err)
	}
	return st, nil
}

// runCertifyAdversarial implements `corral certify --adversarial`: it fires an
// adversarial-pool run against a code+dev-test pair on the brain, polls to
// convergence, renders the signed verdict, and exits by status (0 certified,
// 3 needs-review, 2 usage, 1 infra/timeout). sleep is injected so tests don't
// wait real wall-clock between polls.
func runCertifyAdversarial(args []string, client advPoolClient, run cmdRunner, sleep func(time.Duration), stdout, stderr io.Writer) int {
	flagArgs, checkArgv := splitCertifyArgs(args)

	fs := flag.NewFlagSet("certify --adversarial", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_ = fs.Bool("adversarial", false, "run the adversarial testing pool (this mode)")
	brainURL := fs.String("brain", os.Getenv("CORRAL_BRAIN"), "brain MCP endpoint (or $CORRAL_BRAIN)")
	codePath := fs.String("code", "", "repo-relative path of the code under review (required)")
	testPath := fs.String("test", "", "repo-relative path of the dev's test (default: the _test.go sibling of --code)")
	langFlag := fs.String("lang", "", "source language (default: inferred from --code extension)")
	goal := fs.String("goal", "", "the correctness/security goal the code must satisfy (required)")
	nMutants := fs.Int("n-mutants", 0, "how many seeded-violation mutants (default 5, brain clamps to 20)")
	poll := fs.Duration("poll", 5*time.Second, "how often to poll the run's status")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up waiting for convergence after this long")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url)")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	if strings.TrimSpace(*codePath) == "" {
		fmt.Fprintln(stderr, "corral certify --adversarial: --code is required")
		return 2
	}
	if strings.TrimSpace(*goal) == "" {
		fmt.Fprintln(stderr, "corral certify --adversarial: --goal is required")
		return 2
	}
	if len(checkArgv) == 0 {
		fmt.Fprintln(stderr, "corral certify --adversarial: usage: corral certify --adversarial --code <path> --goal <text> [--test <path>] -- <test command>")
		return 2
	}
	if strings.TrimSpace(*brainURL) == "" {
		fmt.Fprintln(stderr, "corral certify --adversarial: --brain <url> (or $CORRAL_BRAIN) is required")
		return 2
	}

	var plug lang.Plugin
	if strings.TrimSpace(*langFlag) != "" {
		p, ok := lang.ByName(strings.TrimSpace(*langFlag))
		if !ok {
			fmt.Fprintf(stderr, "corral certify --adversarial: unknown --lang %q\n", *langFlag)
			return 2
		}
		plug = p
	} else {
		p, ok := lang.Detect(*codePath)
		if !ok {
			fmt.Fprintf(stderr, "corral certify --adversarial: unknown language for --code %s (pass --lang)\n", *codePath)
			return 2
		}
		plug = p
	}

	tp := strings.TrimSpace(*testPath)
	if tp == "" {
		tp = plug.TestPaths(*codePath)[0].Path
	}

	code, err := os.ReadFile(*codePath) // #nosec G304 -- operator-supplied path to the file under review
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --adversarial: reading --code %s: %v\n", *codePath, err)
		return 2
	}
	devTest, err := os.ReadFile(tp) // #nosec G304 -- operator-supplied (or sibling-derived) test path
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --adversarial: reading test %s: %v (pass --test to override)\n", tp, err)
		return 2
	}

	repo := strings.TrimSpace(*repoFlag)
	if repo == "" {
		if v, gerr := run.GitOutput("config", "--get", "remote.origin.url"); gerr == nil {
			repo = v
		}
	}
	commit := strings.TrimSpace(*commitFlag)
	if commit == "" {
		if v, gerr := run.GitOutput("rev-parse", "HEAD"); gerr == nil {
			commit = v
		}
	}

	spec := advStartSpec{
		Repo: repo, Commit: commit, Goal: strings.TrimSpace(*goal),
		CodePath: *codePath, Code: string(code),
		DevTestPath: tp, DevTestCode: string(devTest),
		TestCmd:  strings.Join(checkArgv, " "),
		NMutants: *nMutants,
		Lang:     plug.Name(),
	}

	ctx := context.Background()
	runID, err := client.StartRun(ctx, *brainURL, spec)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --adversarial: starting run: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "started adversarial run %d — grading %s against its own tests…\n", runID, *codePath)

	deadline := time.Now().Add(*timeout)
	start := time.Now()
	for {
		st, err := client.RunStatus(ctx, *brainURL, runID)
		if err != nil {
			fmt.Fprintf(stderr, "corral certify --adversarial: polling run %d: %v\n", runID, err)
			return 1
		}
		if st.Converged && st.Verdict != nil {
			renderAdvVerdict(stdout, *codePath, *st.Verdict)
			// Hand the test back: when the pool authored a killing test for a gap
			// the dev suite missed, print it so the dev can adopt it. This is the
			// sharing payoff — the herd doesn't just grade the suite, it returns
			// the exact test that closes the proven gap.
			if strings.TrimSpace(st.AuthoredTest) != "" {
				fmt.Fprintf(stdout, "\nthe herd authored a test that catches a gap your suite missed — add it to %s:\n\n", tp)
				fmt.Fprintln(stdout, strings.TrimRight(st.AuthoredTest, "\n"))
			}
			if st.Verdict.Status == "certified" {
				return 0
			}
			return 3
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "corral certify --adversarial: run %d did not converge within %s — re-query later with the brain's get_adversarial_run (run_id %d)\n", runID, *timeout, runID)
			return 1
		}
		fmt.Fprintf(stdout, "  … still running (elapsed %s)\n", time.Since(start).Round(time.Second))
		sleep(*poll)
	}
}

// renderAdvVerdict prints the legible verdict block — the demo artifact. It
// prints exactly what the brain signed; it never upgrades a needs-review to
// CERTIFIED, and shows survivors/proven_missed and the test-critic's pan even
// when unflattering.
func renderAdvVerdict(w io.Writer, codePath string, v advVerdict) {
	status := "NEEDS-REVIEW"
	if v.Status == "certified" {
		status = "CERTIFIED"
	}
	killed := v.MutantsTotal - v.Survivors
	if killed < 0 {
		killed = 0
	}
	commit := v.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	fmt.Fprintf(w, "\nadversarial verdict — %s @ %s\n", codePath, commit)
	if v.Lang != "" {
		fmt.Fprintf(w, "  language:      %s\n", v.Lang)
	}
	if v.TimedOut && !v.DevScored {
		// The pool hit its wall-clock deadline before dev-adequacy ever ran
		// (the mutant-generator itself never finished): DevKillRate/
		// Survivors/MutantsTotal are zero values nothing computed, not a
		// real 0.00 measurement. Checked BEFORE SuiteIgnoresFile/
		// BaselineFailed — both of those are only ever set once dev-adequacy
		// DID run (see runState.devScored's doc in internal/advpool/driver.go),
		// so this is the more fundamental "nothing was measured at all"
		// diagnosis, not merely a competing one.
		fmt.Fprintf(w, "  status:        COULD-NOT-GRADE\n")
		fmt.Fprintf(w, "  reason:        the pool timed out before the dev suite's own tests were ever scored\n")
		fmt.Fprintf(w, "                 (the mutant-generator did not finish before the run's deadline —\n")
		fmt.Fprintf(w, "                 not a test-quality verdict; nothing here was measured)\n")
		return
	}
	if v.SuiteIgnoresFile {
		// The suite PASSED on source that cannot compile: it provably never
		// compiles or imports this file, so every mutant of it "survived" for a
		// reason that has nothing to do with test quality. Checked before
		// BaselineFailed — it is the more specific diagnosis, and it points the
		// operator at the check command rather than at their build.
		fmt.Fprintf(w, "  status:        COULD-NOT-GRADE\n")
		fmt.Fprintf(w, "  reason:        the check command never compiles or imports this file\n")
		fmt.Fprintf(w, "                 (it passed on deliberately invalid source, so no mutant of this file\n")
		fmt.Fprintf(w, "                 could ever have been caught; point --test at a command that runs it)\n")
		fmt.Fprintf(w, "  mutants:       %d generated, 0 graded\n", v.MutantsTotal)
		return
	}
	if v.BaselineFailed {
		// The dev suite did not pass on the UNMUTATED code in the jail: nothing
		// was graded. Reporting DevKillRate/killed/survivors here would present a
		// build/environment failure as a 0% test-quality verdict — the dishonest
		// readout this branch exists to prevent.
		fmt.Fprintf(w, "  status:        COULD-NOT-GRADE\n")
		fmt.Fprintf(w, "  reason:        the dev suite did not pass on the unmutated code in the jail\n")
		fmt.Fprintf(w, "                 (baseline build/test failed — a build/environment issue, not a\n")
		fmt.Fprintf(w, "                 test-quality verdict; e.g. toolchain floor, missing dep, bad --test cmd)\n")
		fmt.Fprintf(w, "  mutants:       %d generated, 0 graded\n", v.MutantsTotal)
		// Print the runner's OWN output, not just the fact of failure. certify
		// --repo has done this since #59; --local computed the identical string
		// and dropped it, so the most common first-run failure — a suite that
		// cannot start inside the jail — arrived with no way to diagnose it.
		if out := strings.TrimSpace(v.BaselineOutput); out != "" {
			fmt.Fprintf(w, "  the suite said:\n%s\n", indentLines(out, "    "))
			if hint := moduleImportHint(out); hint != "" {
				fmt.Fprintf(w, "  %s\n", hint)
			}
		}
		return
	}
	fmt.Fprintf(w, "  status:        %-12s (dev suite killed %d/%d mutants)\n", status, killed, v.MutantsTotal)
	fmt.Fprintf(w, "  dev_kill_rate: %.2f\n", v.DevKillRate)
	fmt.Fprintf(w, "  survivors:     %d\n", v.Survivors)
	if v.MutantsInvalid > 0 {
		// Never hidden. A run whose generator produced mostly unbuildable
		// mutants graded a far smaller exam than the budget implies, and the
		// kill rate above is over the GRADED ones only.
		fmt.Fprintf(w, "  invalid:       %d mutant(s) failed the compile check and were not graded (evidence about the generator, not your tests)\n", v.MutantsInvalid)
	}
	if v.TimedOut {
		// A claim carries how it was earned: dev_kill_rate/survivors above
		// ARE real measurements (the run wouldn't be here, gradable, if they
		// weren't — see Verdict.DevScored), but the pool's remaining
		// "make the tests stronger" half — test-writer, test-critic — never
		// ran. Printing "proven_missed: 0" or "no vacuous tests flagged"
		// here would read as a clean, converged result, which this is not.
		fmt.Fprintln(w, "  TIMED OUT:     the pool did not converge before its deadline")
		if v.PoolScored {
			// A run CAN reach pool-adequacy, prove survivors catchable, and
			// only then stall on the test-critic. That ProvenMissed was earned
			// in the jail like any other, so printing "(not run)" over it
			// discards the strongest evidence this tool produces. Say what
			// actually did not finish instead.
			fmt.Fprintf(w, "  proven_missed: %d (measured — the test-critic never finished)\n", v.ProvenMissed)
		} else {
			fmt.Fprintln(w, "  proven_missed: (not run — pool did not converge)")
		}
	} else {
		fmt.Fprintf(w, "  proven_missed: %d\n", v.ProvenMissed)
	}
	if v.WriterSeatsUngraded > 0 {
		// Printed BESIDE the count it qualifies, never instead of it: the
		// number is real, and this says what it is a count OVER. A file where
		// three of twenty-four seats never graded has a proven_missed that
		// covers twenty-one survivors, not twenty-four.
		fmt.Fprintf(w, "                 (over the graded seats only — %d writer seat(s) never produced a grading test)\n", v.WriterSeatsUngraded)
	}
	if v.TestWriterFailed && v.Survivors > 0 {
		// Honesty: proven_missed=0 here does NOT mean the suite is clean — it
		// means the pool could not author a compiling test to PROVE the gap.
		fmt.Fprintf(w, "  the herd found %d survivor(s) your suite missed but could not author a compiling test to kill them — review these manually\n", v.Survivors)
	}
	if v.PoolTestUnsound && v.Survivors > 0 {
		// A DIFFERENT diagnosis from TestWriterFailed: a compiling test WAS
		// authored, but it never genuinely graded against the survivors (it
		// failed on the unmutated compliant code, or never reads the file).
		// proven_missed=0 here means the same thing TestWriterFailed's 0
		// means — not a clean suite — for a different reason.
		if v.AuthoredTestNotCollected {
			// The narrowed, actionable half. corral PROVED the command never
			// reached the authored test's own file, so this is a problem with
			// the command, not with the test — almost always a command pinned
			// to a single path while the authored test is a NEW file beside
			// the developer's. Saying "or never reads the file" and leaving
			// the operator to guess is how proven_missed reads 0 forever and
			// gets mistaken for "no provable gaps".
			fmt.Fprintf(w, "  the herd authored a test for %d survivor(s), but YOUR TEST COMMAND NEVER RAN IT — the authored test is a new file beside your own, and this command does not collect it (a single-file path, a narrow glob, or a -run filter). Widen the command to its directory and re-run; proven_missed reads 0 only because nothing looked.\n", v.Survivors)
		} else {
			fmt.Fprintf(w, "  the herd authored a test for %d survivor(s) your suite missed, but it did not pass on the unmutated code — it was not scored, review these manually\n", v.Survivors)
		}
	}
	if v.DuplicateMutants > 0 {
		fmt.Fprintf(w, "  %d duplicate mutant(s) collapsed — identical hunks cost one suite run each and measure the same thing once\n", v.DuplicateMutants)
	}
	if v.RegionsTotal > 0 && v.RegionsProbed < v.RegionsTotal {
		fmt.Fprintf(w, "  PARTIAL AUDIT: %d of %d regions probed — these went unprobed: %s\n",
			v.RegionsProbed, v.RegionsTotal, strings.Join(v.DroppedRegions, "; "))
	}
	criticModel := strings.TrimSpace(v.ModelsByRole[advpool.RoleTestCritic])
	criticOff := criticModel == "" || strings.EqualFold(criticModel, "off")
	if v.TimedOut {
		fmt.Fprintln(w, "  critic review: not run — pool did not converge before the critic executed")
	} else if criticOff {
		// The same reasoning as the TIMED OUT branch above, for the seat the
		// operator turned off. "no vacuous tests flagged" reads as a clean bill
		// of health from a reviewer who never looked — an absent check
		// reporting a pass, which is precisely the failure this tool exists to
		// measure in other people's pipelines. Say nothing ran.
		fmt.Fprintln(w, "  critic review: not run — no test-critic was assigned (--critic-model off)")
	} else if len(v.VacuousFindings) == 0 {
		fmt.Fprintln(w, "  critic review: no vacuous tests flagged")
	} else {
		// The critic's flags are a SECOND MODEL'S UNVERIFIED opinion — not part
		// of the execution-proven verdict above, and they do NOT gate the signed
		// record. Label them so no one mistakes an LLM's say-so for proof.
		fmt.Fprintf(w, "  critic review: %d test(s) flagged — UNVERIFIED (a second model's opinion, not execution-proven; check before acting)\n", len(v.VacuousFindings))
	}
	fmt.Fprintf(w, "  models:        %s\n", formatModels(v.ModelsByRole))
	if vendor := soleGradedVendor(v.ModelsByRole); vendor != "" {
		// The fourth participant (#104). CheckDecorrelation keeps corral's own
		// three seats apart and knows nothing about whatever WROTE the code
		// under audit. When every graded seat is one vendor — which is the
		// DEFAULT assignment — and that vendor also wrote the file, the lineage
		// being audited is planting the faults and grading the tests for its
		// own work.
		//
		// The harm at the fault-planter is the quiet one: a model plants the
		// faults it can imagine, which are the cases it already considered
		// while writing the code, so its blind spots go unprobed and the kill
		// rate is optimistic for a reason invisible in the number.
		//
		// Stated, never enforced: corral cannot know what wrote the file, and
		// hand-written code makes this a non-issue. A caveat the reader can
		// dismiss beats a refusal based on a guess.
		fmt.Fprintf(w, "  decorrelation: every graded seat is %s — if this code was WRITTEN by a %s model, the same lineage planted the faults and graded the tests. Point a role at another vendor (--critic-model / --mutant-model) for an independent read.\n", vendor, vendor)
	}
	if v.RecordID != 0 {
		// No inline verify command here: the record lands in a ledger, and
		// `certify verify` reads a self-contained FILE, not a bare id. The
		// --local path prints the precise `--out`-based verify command right
		// after this block when --out is set; the daemon path's record is
		// verifiable via the brain. Printing a bare `verify <record>` here was
		// a dead end (dogfooding, 2026-07-18).
		fmt.Fprintf(w, "  signed:        record %d\n", v.RecordID)
	} else {
		fmt.Fprintln(w, "  signed:        (signing failed — no record id)")
	}
	for _, f := range v.VacuousFindings {
		sev := f.Severity
		if sev == "" {
			sev = "note"
		}
		fmt.Fprintf(w, "      • [%s] %s: %s\n", sev, f.Target, f.Evidence)
	}
}

// formatModels renders ModelsByRole deterministically (sorted by role).
func formatModels(m map[string]string) string {
	if len(m) == 0 {
		return "(none recorded)"
	}
	roles := make([]string, 0, len(m))
	for r := range m {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles))
	for _, r := range roles {
		parts = append(parts, r+"="+m[r])
	}
	return strings.Join(parts, "  ")
}

// soleGradedVendor returns the vendor shared by every GRADED seat, or "" when
// the seats span more than one vendor (or none can be recognized).
//
// The shadow challenger is excluded on purpose: it records a head-to-head
// comparison and never gates a verdict, so its vendor says nothing about the
// independence of the result. An unrecognized model name also yields "" —
// silence is the honest answer when we cannot tell, and a caveat printed on a
// guess would train readers to ignore it.
func soleGradedVendor(models map[string]string) string {
	graded := []string{advpool.RoleMutantGenerator, advpool.RoleTestWriter, advpool.RoleTestCritic}
	vendor := ""
	for _, role := range graded {
		m := strings.TrimSpace(models[role])
		if m == "" || strings.EqualFold(m, "off") {
			continue // a seat that did not run says nothing either way
		}
		v := agentbackend.VendorOf(m)
		if v == "" {
			return ""
		}
		if vendor == "" {
			vendor = v
			continue
		}
		if v != vendor {
			return ""
		}
	}
	return vendor
}
