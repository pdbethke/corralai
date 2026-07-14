// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
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

	// The git-link fields (Task 2): captured via run.GitOutput/GitVerifyCommit
	// at the resolved commit sha, extending the accountability chain to "who
	// signed the code." CommitSignature is always set (never nil) — even an
	// unsigned or git-unavailable commit gets an honest {signed:false,
	// verified:"unsigned"} record, never a silently-dropped field.
	CommitMessage   string         `json:"commit_message,omitempty"`
	CommitAuthor    string         `json:"commit_author,omitempty"`
	CommitDate      string         `json:"commit_date,omitempty"`
	CommitSignature map[string]any `json:"commit_signature,omitempty"`
}

// buildResult is the signed, tamper-evident accountability record the brain
// hands back — mirrors internal/brain's reportBuildOut. PublicKey and Steps
// carry everything `corral certify verify` needs offline: the ledger and
// the certify public key, with no brain round trip.
type buildResult struct {
	ID        int64            `json:"id"`
	Head      string           `json:"head"`
	Signature string           `json:"signature"`
	Statement map[string]any   `json:"statement"`
	PublicKey string           `json:"public_key"`
	Steps     []map[string]any `json:"steps"`
	// Anchored + Rekor carry the transparency-witness evidence report_build
	// produced: whether the DSSE envelope was publicly witnessed, and (when
	// true) the marshaled transparency.Entry a verifier needs to confirm
	// that offline. --out writes both so the exported record is completely
	// self-verifying — see runCertifyVerify's Rekor step.
	Anchored bool   `json:"anchored"`
	Rekor    string `json:"rekor,omitempty"`
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
	// GitVerifyCommit runs `git verify-commit --raw <sha>` and returns its
	// raw output (the GPG/SSH status lines verify-commit emits — on stdout
	// for some mechanisms, stderr for others, so both are captured) and
	// whether the command exited zero. Unlike GitOutput, the raw text is
	// returned even on a nonzero exit: a BADSIG / no-public-key verdict is
	// only visible in that text, discarding it on error would make "bad
	// signature" indistinguishable from "unsigned". A failure to run git at
	// all (not a repo, git/gpg unavailable) is reported via err — callers
	// treat that as "signature status unavailable", never fatal.
	GitVerifyCommit(sha string) (raw string, exitZero bool, err error)
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

func (realRunner) GitVerifyCommit(sha string) (string, bool, error) {
	cmd := exec.Command("git", "verify-commit", "--raw", sha) // #nosec G204 -- fixed "git" binary; sha is corral's own resolved commit, not attacker input
	out, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// git ran and reported a real verdict (or "not signed") via its exit
		// code — the raw text is what parseVerifyCommitRaw needs, not err.
		return strings.TrimSpace(string(out)), false, nil
	}
	// git itself could not be run (not found, not a repo, etc.) — signature
	// status is genuinely unavailable, never fatal to certify.
	return "", false, err
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
	if rec.CommitMessage != "" {
		args["commit_message"] = rec.CommitMessage
	}
	if rec.CommitAuthor != "" {
		args["commit_author"] = rec.CommitAuthor
	}
	if rec.CommitDate != "" {
		args["commit_date"] = rec.CommitDate
	}
	if len(rec.CommitSignature) > 0 {
		args["commit_signature"] = rec.CommitSignature
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

// parseProducedBy splits a comma-separated --produced-by value into its
// trimmed, non-empty entries — shared by the legacy (--brain) and standalone
// certify paths so the flag behaves identically in both.
func parseProducedBy(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
func runCertify(args []string, run cmdRunner, post buildPoster, jail jailRunner, signKey func() (ed25519.PrivateKey, error), stdout, stderr io.Writer) int {
	// `corral certify verify <record-file> ...` is a distinct sub-subcommand
	// (independent, offline verification of an already-produced record) —
	// dispatch it before anything else parses args as certify's own flags.
	if len(args) > 0 && args[0] == "verify" {
		return runCertifyVerify(args[1:], httpPubkeyFetcher, newRekorVerifyWitness, stdout, stderr)
	}

	flagArgs, checkArgv := splitCertifyArgs(args)

	// The standalone path takes an optional leading positional <ref> (e.g.
	// `corral certify HEAD~1 -- go test ./...`). Go's flag package stops
	// parsing at the first non-flag token, so a leading ref would otherwise
	// swallow every flag after it as "positional args" — pull it off first,
	// before fs.Parse ever sees it. Legacy (--brain-first) invocations are
	// unaffected: they never have a leading non-flag token here.
	ref := "HEAD"
	if len(flagArgs) > 0 && !strings.HasPrefix(flagArgs[0], "-") {
		ref = flagArgs[0]
		flagArgs = flagArgs[1:]
	}

	fs := flag.NewFlagSet("certify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	brainURL := fs.String("brain", os.Getenv("CORRAL_BRAIN"), "brain MCP endpoint (or $CORRAL_BRAIN)")
	producedByFlag := fs.String("produced-by", "", "comma-separated models/agents that produced the change under certification")
	outPath := fs.String("out", "", "write the signed statement to this file")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url)")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD)")
	branchFlag := fs.String("branch", "", "branch (default: git rev-parse --abbrev-ref HEAD)")
	netFlag := fs.Bool("net", true, "allow network inside the jail for the check (use --net=false to lock down; standalone path only)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	// A trailing positional (after all flags) also counts as the ref, so
	// `corral certify --out x -- cmd HEAD` style isn't required — but the
	// common/documented form is the leading positional handled above.
	if a := fs.Arg(0); a != "" {
		ref = a
	}

	if len(checkArgv) == 0 {
		fmt.Fprintln(stderr, "corral certify: usage: corral certify --brain <url> [flags] -- <command> [args...]")
		return 2
	}
	if strings.TrimSpace(*brainURL) == "" {
		// No --brain (and no $CORRAL_BRAIN): the standalone path — check out
		// the resolved ref into a clean workspace, run the check jailed, sign
		// the result locally, no brain round trip required.
		return runCertifyStandalone(ref, checkArgv, *outPath, *netFlag, *producedByFlag, run, jail, signKey, stdout, stderr)
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

	// Git-link capture (Task 2): message/author/date via `git show` at the
	// resolved commit sha, and the parsed `git verify-commit --raw` outcome —
	// extending the accountability chain to "who signed the code." Any of
	// these that fail (e.g. certify run outside a git checkout, or against a
	// --commit not present locally) degrade to empty/unsigned rather than
	// failing certify: the check already ran and its result is the point.
	commitMessage := ""
	if commit != "" {
		if v, err := run.GitOutput("show", "-s", "--format=%s", commit); err == nil {
			commitMessage = v
		}
	}
	commitAuthor := ""
	if commit != "" {
		if v, err := run.GitOutput("show", "-s", "--format=%an <%ae>", commit); err == nil {
			commitAuthor = v
		}
	}
	commitDate := ""
	if commit != "" {
		if v, err := run.GitOutput("show", "-s", "--format=%cI", commit); err == nil {
			commitDate = v
		}
	}
	commitSignature := captureCommitSignature(run, commit)

	producedBy := parseProducedBy(*producedByFlag)

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

		CommitMessage:   commitMessage,
		CommitAuthor:    commitAuthor,
		CommitDate:      commitDate,
		CommitSignature: commitSignature,
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
		// The FULL record — not just the bare statement — so `corral certify
		// verify <file>` can check it completely offline: no brain round
		// trip needed to recover the signature, the ledger, or the pubkey.
		if err := writeRecord(*outPath, result); err != nil {
			fmt.Fprintf(stderr, "corral certify: %v\n", err)
			return exitCode
		}
	}

	return exitCode
}

// gpgStatusLineRe matches a GPG "--status-fd"-style status line for a given
// tag (e.g. GOODSIG, BADSIG, NO_PUBKEY, ERRSIG): "[GNUPG:] <TAG> <keyid>
// [rest...]". The captured group is everything after the keyid — the
// human-readable signer identity for GOODSIG/BADSIG, empty for tags that
// don't carry one (NO_PUBKEY, ERRSIG).
func gpgStatusLineRe(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\[GNUPG:\] ` + tag + ` [0-9A-Fa-f]+ ?(.*)$`)
}

// sshGoodSigRe matches the plain-text form `git verify-commit` prints for an
// SSH-signed commit (via ssh-keygen -Y verify, which doesn't speak the GPG
// status-fd protocol): `Good "git" signature for <signer> with ...`.
var sshGoodSigRe = regexp.MustCompile(`Good "git" signature for (\S+)`)

// captureCommitSignature runs `git verify-commit --raw <sha>` via run and
// parses the result into the honest {signed, signer, mechanism, verified}
// shape report_build expects. Any failure to run git at all (not a repo,
// git/gpg unavailable, sha not resolvable) degrades to {signed:false,
// verified:"unsigned"} — signature status being unavailable is never fatal
// to certify.
func captureCommitSignature(run cmdRunner, sha string) map[string]any {
	if sha == "" {
		return map[string]any{"signed": false, "verified": "unsigned"}
	}
	raw, exitZero, err := run.GitVerifyCommit(sha)
	if err != nil {
		return map[string]any{"signed": false, "verified": "unsigned"}
	}
	return parseVerifyCommitRaw(raw, exitZero)
}

// parseVerifyCommitRaw classifies the raw output of `git verify-commit
// --raw` into the {signed, signer, mechanism, verified} shape. The honesty
// contract (never faked green): an unsigned commit or output we can't
// positively classify as GOOD/BAD/unknown-key comes back verified:"unsigned"
// (or the specific bad/unknown-key verdict) — it is NEVER reported as
// "good" unless the raw text actually says so.
func parseVerifyCommitRaw(raw string, exitZero bool) map[string]any {
	raw = strings.TrimSpace(raw)
	mechanism := inferSignatureMechanism(raw)

	if signer, ok := gpgStatusSigner(raw, "GOODSIG"); ok && exitZero {
		return signatureResult("good", signer, mechanism)
	}
	if exitZero {
		if m := sshGoodSigRe.FindStringSubmatch(raw); m != nil {
			return signatureResult("good", m[1], mechanism)
		}
	}
	if raw == "" {
		// verify-commit exited nonzero with no status output at all — the
		// commit simply isn't signed, not a verification failure.
		return map[string]any{"signed": false, "verified": "unsigned"}
	}
	if signer, ok := gpgStatusSigner(raw, "NO_PUBKEY"); ok {
		return signatureResult("unknown-key", signer, mechanism)
	}
	if signer, ok := gpgStatusSigner(raw, "BADSIG"); ok {
		return signatureResult("bad", signer, mechanism)
	}
	if signer, ok := gpgStatusSigner(raw, "ERRSIG"); ok {
		// ERRSIG covers several distinct GPG failure modes (unknown key,
		// unsupported algorithm, ...) that all mean "couldn't be positively
		// verified" — never promote this to "bad" (that implies a
		// deliberately forged signature) or "good".
		return signatureResult("unknown-key", signer, mechanism)
	}
	// Non-empty output we can't positively classify — never invent "good";
	// treat conservatively as unsigned rather than guess at a verdict.
	return map[string]any{"signed": false, "verified": "unsigned"}
}

func gpgStatusSigner(raw, tag string) (string, bool) {
	m := gpgStatusLineRe(tag).FindStringSubmatch(raw)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

func inferSignatureMechanism(raw string) string {
	switch {
	case strings.Contains(raw, "gitsign"):
		return "gitsign"
	case strings.Contains(raw, `"git" signature`):
		return "ssh"
	case strings.Contains(raw, "[GNUPG:]"):
		return "gpg"
	default:
		return ""
	}
}

func signatureResult(verified, signer, mechanism string) map[string]any {
	m := map[string]any{"signed": true, "verified": verified}
	if signer != "" {
		m["signer"] = signer
	}
	if mechanism != "" {
		m["mechanism"] = mechanism
	}
	return m
}
