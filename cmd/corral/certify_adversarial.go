// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/brainclient"
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
	Repo            string            `json:"Repo"`
	Commit          string            `json:"Commit"`
	DevKillRate     float64           `json:"DevKillRate"`
	MutantsTotal    int               `json:"MutantsTotal"`
	Survivors       int               `json:"Survivors"`
	ProvenMissed    int               `json:"ProvenMissed"`
	VacuousFindings []advFinding      `json:"VacuousFindings"`
	ModelsByRole    map[string]string `json:"ModelsByRole"`
	Status          string            `json:"Status"`
	RecordID        int64             `json:"RecordID"`
	RecordHead      string            `json:"RecordHead"`
}

// advStatus mirrors brain.AdvPoolStatusOut (get_adversarial_run's output).
type advStatus struct {
	RunID     int64       `json:"run_id"`
	Found     bool        `json:"found"`
	Converged bool        `json:"converged"`
	Verdict   *advVerdict `json:"verdict"`
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
		"test_cmd": spec.TestCmd,
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

	tp := strings.TrimSpace(*testPath)
	if tp == "" {
		tp = siblingTestPath(*codePath)
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

// siblingTestPath derives foo.go -> foo_test.go in the same directory.
func siblingTestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	return base + "_test" + ext
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
	fmt.Fprintf(w, "  status:        %-12s (dev suite killed %d/%d mutants)\n", status, killed, v.MutantsTotal)
	fmt.Fprintf(w, "  dev_kill_rate: %.2f\n", v.DevKillRate)
	fmt.Fprintf(w, "  survivors:     %d\n", v.Survivors)
	fmt.Fprintf(w, "  proven_missed: %d\n", v.ProvenMissed)
	if len(v.VacuousFindings) == 0 {
		fmt.Fprintln(w, "  vacuous tests: none flagged")
	} else {
		fmt.Fprintf(w, "  vacuous tests: %d flagged\n", len(v.VacuousFindings))
	}
	fmt.Fprintf(w, "  models:        %s\n", formatModels(v.ModelsByRole))
	if v.RecordID != 0 {
		fmt.Fprintf(w, "  signed:        record %d  (verify offline: corral certify verify <record>)\n", v.RecordID)
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
