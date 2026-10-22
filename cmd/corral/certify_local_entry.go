// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// A `certify --local` run's ledger entry. Until this file, a --local run
// wrote a signed build record into the build store and nothing else: no
// ledger entry, so nothing a reader of the record — `corral verify --ledger`,
// the warehouse, a prior — could ever see. The run audits ONE file, and the
// entry is that one file as a one-file scan, built by the SAME row builders a
// repo scan's entry is (buildScanFileRows, buildScanMutantRows,
// buildScanModelCallRows, buildBundle, telemetryRows, stampHunks) over the
// verdict wrapped as a reposcan.FileResult. A second, --local-only mapping
// was the alternative and was rejected for the reason certify_repo_bundle.go
// gives: two mappings of one record are how two records start to disagree,
// and every field added to the repo path would then have to be remembered
// here too.
//
// What --local does NOT have, and the entry therefore does not claim: no
// candidate enumeration (TotalFiles is unmeasured, so 0 and NULL), no
// preflight, no verdict cache (no CacheKey: an entry from --local is never
// served back as a cached verdict), no scan-wide event grain (the --record
// tape is its own artefact, in the tape's shape, not scanstore.Event rows),
// and no gates (MinKillRate/MaxProvenMissed are nil — the verdict's own
// status decides the exit code).

// localRunFacts are what a --local entry records that the verdict does not
// carry and that must be measured OUTSIDE auditOneFile.
type localRunFacts struct {
	// treeDirty is asked of git ONCE, before the audit starts (treeDirtyAt's
	// rule): nil when git could not say, or when the recorded commit is not
	// HEAD — never a guessed false.
	treeDirty *bool
	// repoIdentity is how the subject's repo name was established, decided
	// with the subject itself (resolveSubject) before the audit starts:
	// "git" when derived from --repo-dir's git, "operator" when typed as
	// --repo/--commit, "none" when it could not be derived or the run is
	// unattributed. Carried here rather than re-derived at the end, so it
	// describes the subject the verdict signed, not a later look at git.
	repoIdentity string
	// sandbox is the jail the audit ran in (localSandboxIdentity).
	sandbox sandbox.Identity
	// mutantsFrom is the sha256 of the --mutants document the run replayed,
	// "" when it generated its own exam — the same column a repo scan fills.
	mutantsFrom string
	// recorder holds the hunks of the mutants the run graded, stamped onto
	// the entry's mutant rows. nil leaves the rows without code (a run that
	// recorded nothing), which is what the repo path does for a cache hit.
	recorder *mutantSetRecorder
}

// localFileResult wraps the one verdict a --local run produced as the
// reposcan.FileResult a repo scan would have recorded for it, so the repo
// scan's row builders apply unchanged. Path is the run's codeKey —
// localMutantSetKey, the same name the driver handed the mutant sink, so the
// hunks the recorder holds match the mutant rows by (path, id). Gradable and
// Reason follow localExecutor.Execute's rule exactly: a baseline that failed,
// or a suite that never touches the file, is ungradable WITH its reason,
// never a 0.0 kill rate.
func localFileResult(in localAuditInput, v advpool.Verdict, t advpool.RunTelemetry, at time.Time) reposcan.FileResult {
	// The discarded value is the file's filesystem path, not an error:
	// localMutantSetKey returns none. Its one degraded case (a --code that
	// filepath.Rel cannot place under --repo-dir) falls back to the
	// operator's spelling, and buildJailWiring refuses that run before any
	// verdict exists, so no entry is ever built from it.
	key, _ := localMutantSetKey(in.repoDir, in.codePath)
	res := reposcan.FileResult{
		Job:     reposcan.Job{Path: key, Lang: v.Lang},
		Verdict: v, Gradable: !v.BaselineFailed && !v.SuiteIgnoresFile,
		ComputedAt: at, Telemetry: t,
	}
	switch {
	case v.SuiteIgnoresFile:
		res.Reason = reposcan.ReasonSuiteIgnoresFile
	case v.BaselineFailed:
		res.Reason = reposcan.ReasonBaselineFailed
	}
	return res
}

// localEntryBundle is the complete ledger entry for one finished --local run:
// the scan header, the one file row, its mutant rows (with their hunks when
// the run recorded them), its model calls and the three telemetry slices.
// Built AFTER auditOneFile has set v.ModelCalls, so the calls and the
// header's token totals are present. The subject is the VERDICT's own
// (v.Repo, v.Commit) — the one the run resolved once before it started and
// signed — and is never asked of git again here: a second look at HEAD after
// a long run names whatever the operator committed meanwhile, which put a new
// revision on the entry's header and telemetry rows around a verdict, build
// record and scorecard rows signed for the old one (final whole-branch
// review, 2026-10-20). The audited party (CommitIdentity) is read for that
// same signed commit.
func localEntryBundle(in localAuditInput, v advpool.Verdict, t advpool.RunTelemetry, started time.Time, run localRunFacts) (auditpush.Bundle, error) {
	repo, commit := v.Repo, v.Commit
	finished := time.Now()
	results := []reposcan.FileResult{localFileResult(in, v, t, finished)}
	// The run's own stderr, so a row builder's disclosure (a verdict that
	// would not marshal, mutants disagreeing about their parent) reaches the
	// operator instead of vanishing.
	stderr := in.stderr
	if stderr == nil {
		stderr = io.Discard
	}
	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, run.mutantsFrom, in.repoDir, stderr)
	mutants := buildScanMutantRows(0, results)
	calls := buildScanModelCallRows(results)
	inTokens, outTokens, callCount := modelCallRowTotals(calls)
	audited := 0
	if results[0].Gradable {
		audited = 1
	}
	// The kill rate the file row already decided to record (nil for an
	// ungradable file, or one whose every mutant the compile gate threw
	// out) — never a second derivation of it.
	var killRate *float64
	if len(files) == 1 {
		killRate = files[0].KillRate
	}
	// The audited party. Only a checkout can say who made a commit; with no
	// --repo-dir, `git -C ""` would ask the process's working directory,
	// which is some other repository's history.
	var party auditpush.Identity
	if strings.TrimSpace(in.repoDir) != "" {
		party = auditpush.CommitIdentity(in.repoDir, commit)
	}
	host, _ := os.Hostname()
	scan := scanstore.Scan{
		Repo: repo, Commit: commit,
		// --local has no --substrate: it always runs in the jail.
		Substrate:     substrateJail,
		EngineVersion: version,
		ModelSet:      modelSetKeyWith(in.toolProbe, in.writerModel, in.mutantModel, in.criticModel, in.shadowModel, in.shadowWriterModel),
		Candidates:    1, Audited: audited,
		KillRate:  killRate,
		StartedAt: started, FinishedAt: finished,
		Author: party.Author, Committer: party.Committer, CoAuthors: party.CoAuthors,
		CorralVersion: version, Host: host, Cores: runtime.NumCPU(),
		TotalMillis: finished.Sub(started).Milliseconds(),
		InputTokens: inTokens, OutputTokens: outTokens, ModelCalls: callCount,
	}
	rosterJSON, _ := json.Marshal(v.ModelsByRole)
	seatToolsJSON := ""
	if len(v.SeatTools) > 0 {
		b, _ := json.Marshal(v.SeatTools)
		seatToolsJSON = string(b)
	}
	// sourcePushed true: the entry carries its source (the verdict JSON, the
	// authored test, the hunks) because it IS the record — the repo scan's
	// ledger entry does the same — and it is written only to directories on
	// this box.
	b := buildBundle(scan, 0, files, mutants, calls, nil, auditpush.Link{}, true, repo, commit, githubRunURL(),
		bundleMeta{
			ModelsByRole: string(rosterJSON), SeatTools: seatToolsJSON,
			Passed:    boolPtr(v.Status == advpool.StatusCertified),
			PushedBy:  auditpush.PushedByCertify,
			TreeDirty: run.treeDirty, RepoIdentity: run.repoIdentity,
			Sandbox: run.sandbox,
		})
	if run.recorder != nil {
		run.recorder.stampHunks(b.Mutants)
	}
	b.Seats, b.MutantAttempts, b.CriticFindings = telemetryRows(repo, commit, results[0].Job.Path, t)
	return b, nil
}

// writeLocalLedgerEntries writes a --local run's entry to the machine ledger
// (unless --no-ledger) and to the operator's --ledger directory (when given,
// and when it is not the machine ledger under another spelling — one run is
// one entry in one chain). One line per entry written:
//
//	ledger: <file> in <dir> (<signed by keyid | unsigned>)
//
// Fail-open without exception: a write that fails is printed on stderr and
// costs that copy only; the verdict and the exit code are the audit's. A key
// that cannot be used is selectLedgerSigner's refusal — nothing is
// written unsigned, because an unsigned entry is permanent in a hash-linked
// chain — printed the same way. (A repo scan turns a green run red on that
// refusal for its PRIMARY entry; --local does not, because by the time it
// writes, the same key has already signed the build record, so an unusable
// key here cannot be the ordinary case, and a ledger write is not allowed to
// decide a verdict.)
//
// The machine ledger is held to the same rule as --ledger: when
// CORRALAI_MACHINE_LEDGER (or CORRALAI_STATE_DIR) puts it inside repoDir,
// the entry is NOT written there — not even the directory is created — and
// the skip is printed with its reason. --local never writes into the
// checkout, whoever chose the path.
func writeLocalLedgerEntries(b auditpush.Bundle, ledgerFlag string, noLedger bool, repoDir string, stdout, stderr io.Writer) {
	machine := ""
	switch {
	case noLedger:
		fmt.Fprintln(stdout, "  ledger: --no-ledger — no entry written to the machine ledger")
	default:
		// Resolved and checked BEFORE machineLedgerDir creates it, so a
		// machine ledger declared inside the checkout leaves no trace there.
		dir, ok := machineLedgerOutsideCheckout("corral certify --local", "--repo-dir", repoDir, stderr)
		if !ok {
			break
		}
		machine = dir
		writeLocalLedgerEntry(b, dir, stdout, stderr)
	}
	if extra := strings.TrimSpace(ledgerFlag); extra != "" {
		if machine != "" && samePath(machine, extra) {
			return
		}
		writeLocalLedgerEntry(b, extra, stdout, stderr)
	}
}

func writeLocalLedgerEntry(b auditpush.Bundle, dir string, stdout, stderr io.Writer) {
	c, err := pushBundle(strings.TrimRight(dir, "/")+"/", b, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: writing the ledger entry to %s: %v\n", dir, err)
		return
	}
	if c.Entry == "" {
		return
	}
	signed := "unsigned"
	if c.KeyID != "" {
		signed = "signed by " + c.KeyID
	}
	fmt.Fprintf(stdout, "  ledger: %s in %s (%s)\n", c.Entry, dir, signed)
}

// ledgerInsideCheckout reports whether dir lies inside repoDir, compared
// absolute and with symlinks resolved as far as they exist (samePath's
// rule), so neither a relative spelling nor a link can carry an entry into
// the checkout. --local promises never to write into the checkout, and an
// operator's --ledger is held to that promise rather than excepted from it.
func ledgerInsideCheckout(dir, repoDir string) bool {
	if strings.TrimSpace(repoDir) == "" {
		return false
	}
	canon := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		// Resolve the longest prefix that exists: dir is usually not created
		// yet, and its parent may be a link into the checkout.
		rest := ""
		for q := p; ; q = filepath.Dir(q) {
			if real, err := filepath.EvalSymlinks(q); err == nil {
				return filepath.Join(real, rest)
			}
			if filepath.Dir(q) == q {
				return filepath.Clean(p)
			}
			rest = filepath.Join(filepath.Base(q), rest)
		}
	}
	rel, err := filepath.Rel(canon(repoDir), canon(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// localSandboxIdentity names the jail a finished --local audit ran in, for its
// entry. It resolves --jail again rather than threading the isolator out of
// the audit: the resolution already succeeded there (the audit ran), and doing
// it up front instead would change which error a host without a sandbox
// reports first. For a container the image is in the runtime's store by now,
// so its ID is the one that ran. If it still cannot be identified the entry
// records no sandbox, which reads as not recorded, and says so.
func localSandboxIdentity(jailFlag string, stderr io.Writer) sandbox.Identity {
	iso, err := resolveLocalJail(jailFlag)
	var id sandbox.Identity
	if err == nil {
		id, err = sandbox.Identify(iso)
	}
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: the entry will not name its jail: %v\n", err)
		return sandbox.Identity{}
	}
	return id
}
