// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/brainclient"
	"github.com/pdbethke/corralai/internal/creds"
)

// buildRecord is the raw build record `corral certify` reports to the brain's
// report_build tool — the wire shape mirrors internal/brain's reportBuildIn.
type buildRecord struct {
	Repo         string   `json:"repo"`
	Commit       string   `json:"commit"`
	Branch       string   `json:"branch,omitempty"`
	Command      string   `json:"command"`
	ExitCode     int      `json:"exit_code"`
	DurationS    float64  `json:"duration_s,omitempty"`
	OutputDigest string   `json:"output_digest,omitempty"`
	ProducedBy   []string `json:"produced_by,omitempty"`
}

// buildResult is the signed, tamper-evident accountability record the brain
// hands back — mirrors internal/brain's reportBuildOut.
type buildResult struct {
	ID        int64          `json:"id"`
	Head      string         `json:"head"`
	Signature string         `json:"signature"`
	Statement map[string]any `json:"statement"`
}

// cmdRunner is how runCertify executes shell work: cheap git-context lookups
// and the one command actually being certified. Real work goes through
// realRunner (os/exec); tests inject a fake so no git repo or subprocess is
// needed to exercise the exit-passthrough and record-building logic.
type cmdRunner interface {
	// GitOutput runs `git <args...>` in the current directory and returns its
	// trimmed stdout. A failure (e.g. not a git repo) is reported via err;
	// callers treat that as "context unavailable", not a fatal error.
	GitOutput(args ...string) (string, error)
	// RunCommand runs argv, streaming its stdout/stderr live to the given
	// writers while also capturing the combined output for the build digest.
	// err is non-nil only when the command could not be run at all (e.g. not
	// found) — a normal nonzero exit is reported via the returned exit code,
	// not err.
	RunCommand(argv []string, stdout, stderr io.Writer) (exitCode int, dur time.Duration, combined []byte, err error)
}

// buildPoster reports a completed build record to a brain and gets back the
// signed accountability record. The real implementation (mcpPoster) dials the
// brain's MCP endpoint and calls report_build; tests inject a fake that just
// captures what was posted.
type buildPoster interface {
	Post(ctx context.Context, brainURL string, rec buildRecord) (buildResult, error)
}

// realRunner is cmdRunner backed by os/exec.
type realRunner struct{}

func (realRunner) GitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output() // #nosec G204 -- fixed "git" binary; args are corral's own literal subcommands
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (realRunner) RunCommand(argv []string, stdout, stderr io.Writer) (int, time.Duration, []byte, error) {
	var combined bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- argv is the operator-supplied check command, the entire point of `corral certify -- <cmd>`
	cmd.Stdout = io.MultiWriter(stdout, &combined)
	cmd.Stderr = io.MultiWriter(stderr, &combined)
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), dur, combined.Bytes(), nil
	}
	if err != nil {
		return -1, dur, combined.Bytes(), err
	}
	return 0, dur, combined.Bytes(), nil
}

// mcpPoster is buildPoster backed by a real MCP call to the brain's
// report_build tool. The bearer token is resolved fresh on every Post from
// the credential keystore (env -> OS keyring -> age file) — never logged,
// never printed.
type mcpPoster struct{}

func (mcpPoster) Post(ctx context.Context, brainURL string, rec buildRecord) (buildResult, error) {
	token, err := brainToken()
	if err != nil {
		return buildResult{}, fmt.Errorf("resolve brain token: %w", err)
	}
	cl, err := brainclient.Dial(ctx, brainURL, token)
	if err != nil {
		return buildResult{}, err
	}
	defer func() { _ = cl.Close() }()

	args := map[string]any{
		"repo":      rec.Repo,
		"commit":    rec.Commit,
		"branch":    rec.Branch,
		"command":   rec.Command,
		"exit_code": rec.ExitCode,
	}
	if rec.DurationS != 0 {
		args["duration_s"] = rec.DurationS
	}
	if rec.OutputDigest != "" {
		args["output_digest"] = rec.OutputDigest
	}
	if len(rec.ProducedBy) > 0 {
		args["produced_by"] = rec.ProducedBy
	}

	res, err := cl.CallTool(ctx, "report_build", args)
	if err != nil {
		return buildResult{}, err
	}
	text := brainclient.FirstText(res)
	if res.IsError {
		msg := strings.TrimSpace(text)
		if msg == "" {
			msg = "report_build reported an error"
		}
		return buildResult{}, fmt.Errorf("%s", msg)
	}

	var out buildResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return buildResult{}, fmt.Errorf("decoding report_build response: %w", err)
	}
	return out, nil
}

// brainToken resolves the bearer token `corral certify` authenticates to the
// brain with, from the same portable credential keystore `corral secret`
// manages (env -> OS keyring -> age file).
//
// This deliberately reads CORRALAI_BRAIN_TOKEN, NOT CORRALAI_BRAIN_KEY:
// main.go documents CORRALAI_BRAIN_KEY as the Ed25519 IDENTITY SEED for
// cross-swarm brain identity (a private key), a completely different
// secret from an HTTP bearer token. Reusing it here would collide two
// unrelated secrets under one name.
func brainToken() (string, error) {
	s, err := creds.Open()
	if err != nil {
		return "", err
	}
	v, ok, err := s.Get("CORRALAI_BRAIN_TOKEN")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no CORRALAI_BRAIN_TOKEN configured (run: corral secret set CORRALAI_BRAIN_TOKEN)")
	}
	return v, nil
}

// splitCertifyArgs splits on the first bare "--": everything before is
// corral certify's own flags, everything after is the checked command's argv.
func splitCertifyArgs(args []string) (flags, checkArgv []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// runCertify implements `corral certify --brain <url> [flags] -- <command...>`:
// it resolves git/CI context, RUNS the check itself (streaming its output
// live while capturing a digest), reports the result to the brain's
// report_build tool, prints the returned record id/head, optionally writes
// the signed statement to --out, and ALWAYS returns the check's own exit
// code — a failed check is still recorded ("did not pass, here's the
// proof"), and a failure to reach the brain never masks or flips a real
// build result.
func runCertify(args []string, run cmdRunner, post buildPoster, stdout, stderr io.Writer) int {
	flagArgs, checkArgv := splitCertifyArgs(args)

	fs := flag.NewFlagSet("certify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	brainURL := fs.String("brain", os.Getenv("CORRAL_BRAIN"), "brain MCP endpoint (or $CORRAL_BRAIN)")
	producedByFlag := fs.String("produced-by", "", "comma-separated models/agents that produced the change under certification")
	outPath := fs.String("out", "", "write the signed statement to this file")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url)")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD)")
	branchFlag := fs.String("branch", "", "branch (default: git rev-parse --abbrev-ref HEAD)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	if len(checkArgv) == 0 {
		fmt.Fprintln(stderr, "corral certify: usage: corral certify --brain <url> [flags] -- <command> [args...]")
		return 2
	}
	if strings.TrimSpace(*brainURL) == "" {
		fmt.Fprintln(stderr, "corral certify: --brain (or $CORRAL_BRAIN) is required")
		return 2
	}

	repo := strings.TrimSpace(*repoFlag)
	if repo == "" {
		if v, err := run.GitOutput("config", "--get", "remote.origin.url"); err == nil {
			repo = v
		}
	}
	commit := strings.TrimSpace(*commitFlag)
	if commit == "" {
		if v, err := run.GitOutput("rev-parse", "HEAD"); err == nil {
			commit = v
		}
	}
	branch := strings.TrimSpace(*branchFlag)
	if branch == "" {
		if v, err := run.GitOutput("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			branch = v
		}
	}

	var producedBy []string
	if v := strings.TrimSpace(*producedByFlag); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				producedBy = append(producedBy, p)
			}
		}
	}

	command := strings.Join(checkArgv, " ")
	exitCode, dur, combined, runErr := run.RunCommand(checkArgv, stdout, stderr)
	if runErr != nil {
		fmt.Fprintf(stderr, "corral certify: running %q: %v\n", command, runErr)
		return 1
	}

	sum := sha256.Sum256(combined)
	rec := buildRecord{
		Repo:         repo,
		Commit:       commit,
		Branch:       branch,
		Command:      command,
		ExitCode:     exitCode,
		DurationS:    dur.Seconds(),
		OutputDigest: "sha256:" + hex.EncodeToString(sum[:]),
		ProducedBy:   producedBy,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, postErr := post.Post(ctx, *brainURL, rec)
	if postErr != nil {
		// A failed report to the brain must never mask or flip the check's
		// own result — the check already ran; this is telemetry, not gate.
		fmt.Fprintf(stderr, "corral certify: recording build to brain: %v\n", postErr)
		return exitCode
	}

	fmt.Fprintf(stdout, "certified build %d: head=%s\n", result.ID, result.Head)

	if *outPath != "" {
		b, err := json.MarshalIndent(result.Statement, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "corral certify: marshaling statement: %v\n", err)
			return exitCode
		}
		if err := os.WriteFile(*outPath, b, 0o644); err != nil { // #nosec G306 -- the signed statement is meant to be shared/attached to CI artifacts, not secret
			fmt.Fprintf(stderr, "corral certify: writing %s: %v\n", *outPath, err)
			return exitCode
		}
	}

	return exitCode
}
