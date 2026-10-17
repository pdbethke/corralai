// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/clip"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/review"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// runReview implements `corral review`: a cold reviewer seat over a scope,
// its reproductions run, the record written beside the audits — and the
// two verbs that read and adjudicate it. See internal/review and
// docs/design/adversarial-review.md; this is the one-week slice: no
// verifier seat, the human adjudicating in the ledger.
func runReview(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || wantsHelp(args[:1]) {
		fmt.Fprint(stdout, reviewUsage)
		// The run verb's own flags, so the CLI reference (generated from
		// -h) and the surface manifest see every one of them.
		fs, _ := reviewFlagSet(stdout)
		fmt.Fprintln(stdout, "\nflags of `corral review`:")
		fs.PrintDefaults()
		return 0
	}
	switch args[0] {
	case "adjudicate":
		return runReviewAdjudicate(args[1:], stdout, stderr)
	case "show":
		return runReviewShow(args[1:], stdout, stderr)
	case "plan":
		return runReviewPlan(args[1:], stdout, stderr)
	case "recheck":
		return runReviewRecheck(args[1:], stdout, stderr)
	case "churn":
		return runReviewChurn(args[1:], stdout, stderr)
	}
	return runReviewRun(args, stdout, stderr)
}

const reviewUsage = `corral review — a cold model reviews a scope of the repository; corral runs its reproductions and records the review beside the audits.

  corral review --scope <dir|file> --reviewer-model <m> [--verifier-model <m2>] [--repo <dir>] [flags]
      The reviewer is told to assume the code is wrong. Every finding carries a tier it
      declared — REPRODUCED (with a sh script that exits 0 iff the defect is demonstrated),
      CODE-READ (file:line, argued), HYPOTHESIS — and the run executes every REPRODUCED
      script in the offline sandbox certify grades in, over the tree at HEAD (refused when
      there is none; --substrate workspace runs them in a detached worktree on the host, for
      a CI runner). A script that does not hold demotes its
      finding to CODE-READ on the record, out loud. The reviewer must also list what it
      checked and found sound. The opinion is printed and carried; only the reproductions
      are what the entry's signature vouches for. Exit 0 either way unless --fail-on reproduced,
      which exits 3 when a REPRODUCED finding stands after the run — the record is written first.
      With --verifier-model, a THIRD model (never the reviewer's) tries to refute every finding by
      the same rules; a refutation whose script holds demotes the finding, and is itself recorded.
      flags: --ledger <dir> (default <repo>/.corral/ledger)  --no-ledger  --timeout 60s
             --max-bytes 200000 (how much of the scope the reviewer is shown)  --fail-on reproduced
  corral review plan [--repo <dir>] [--ledger <dir>] [--depth 2]
      The round planner: every scope of the repository, when the ledger last saw it reviewed, its
      findings by outcome, how many of its files changed since — and a proposal for the next round:
      never reviewed first, then changed since review (a fix batch nobody re-attacked), then the
      stalest. No model runs; a person names the scope.
  corral review churn [--repo <dir>] [--ledger <dir>] [--findings] [--json]
      Is the review loop draining the code or churning it? For every review on the record,
      each finding's line is blamed at the reviewed commit: DRAIN if the line was there when
      the previous review of its file looked, CHURN if it was written since — counting every
      change in between, since the record does not say which commits were the fixes —
      FIRST LOOK if no earlier review covered the file. Findings that fell are not counted.
      No model runs; nothing is written.
  corral review show <ledger dir> <review hash>       print a review with its adjudications applied
  corral review adjudicate <ledger dir> <hash>#<Rn> --confirm|--refute --reason "…" [--by <who>]
      A person's verdict on one finding, as its own entry: the newest verdict per finding
      stands; automatic passes never write one. --by defaults to the OS user.
  corral review recheck <ledger dir> <hash>#<Rn> [--repo <dir>] [--timeout 1m] [--json]
      Re-run one finding's recorded script against the CURRENT HEAD of --repo, in a disposable
      worktree: still-reproduces (exit 0), no-longer-reproduces (any other exit), or could-not-run
      (a harness error, a timeout, exit 126/127). Writes nothing; quote it in an adjudication's
      --reason to put it on the record. Exit 0 when it ran, 3 when it could not.
`

// reviewFlags is the run verb's flag set, bound in one place so -h and the
// run agree about every flag.
type reviewFlags struct {
	substrate                                                     string
	repoDir, scope, model, verifier, ledger, attest, push, failOn string
	noLedger, pushSource                                          bool
	timeout                                                       time.Duration
	maxBytes                                                      int
}

// reviewExitStanding is the exit code --fail-on turns a standing finding
// into: distinct from 1 (the run could not complete) and 2 (usage), so a
// gate can tell "the review found something that reproduced" from "the
// review did not happen".
const reviewExitStanding = 3

func reviewFlagSet(out io.Writer) (*flag.FlagSet, *reviewFlags) {
	fs := flag.NewFlagSet("corral review", flag.ContinueOnError)
	fs.SetOutput(out)
	f := &reviewFlags{}
	fs.StringVar(&f.repoDir, "repo", ".", "the checkout to review (a git repository at a commit)")
	fs.StringVar(&f.scope, "scope", "", "the directory or file under --repo to review (required)")
	fs.StringVar(&f.model, "reviewer-model", "", "the reviewer seat — an alias from the registry, a provider model name, or an AGENTIC seat: an agent NAME, pinned to a model as `<name>:<model>`. `claude-code`, `codex` and `antigravity` are defined; ANY agent is defined the same way as CORRALAI_AGENT_<NAME>=\"<command line>\" ({dir} the worktree, {out} a reply file, {model} / {model:FLAG} the pin; the brief on stdin, the reply on stdout). An agentic seat is a coding agent started in a disposable copy of the repository: it reads the whole scope itself (no --max-bytes cap) and hands back scripts, which corral runs — nothing it did itself is on the record. Required; corral has no default models")
	fs.StringVar(&f.verifier, "verifier-model", "", "a VERIFIER seat, adversarial to the reviewer: a different model that tries to refute every finding, by the same rules — a REPRODUCED refutation (a sh script that exits 0 iff the refutation is demonstrated) that holds demotes the finding on the record; a CODE-READ refutation is carried as opinion; a search that finds nothing is never a refutation. Must not be the reviewer's model. Off unless named")
	fs.StringVar(&f.ledger, "ledger", "", "the ledger directory the review entry is written to (default: <repo>/.corral/ledger, or $CORRAL_LEDGER)")
	fs.StringVar(&f.attest, "attest", "", "write an in-toto statement (predicate https://corralai.dev/review/v1) to this path, and its DSSE envelope beside it when a certify key is configured: the REPRODUCTIONS — every finding's declared and recorded tier, the hash of its script and output, its exit, the verifier's refutation on the same terms — signed; the opinion bound by its hash and not carried. The ledger entry then names the statement. `corral verify --attest <path> --db <ledger dir>` recomputes the reproductions' hash from the entry")
	fs.StringVar(&f.push, "push", "", "also append the review's rows to a warehouse you own — a DuckDB path, or md:<db> — as corral_reviews and corral_findings, keyed by the entry's hash so a later `review adjudicate --push` joins to them. Scripts and outputs travel as hashes unless --push-source")
	fs.BoolVar(&f.pushSource, "push-source", false, "with --push, also send the scripts and what they printed (they quote the audited code); off by default")
	fs.BoolVar(&f.noLedger, "no-ledger", false, "print the review and write no entry")
	fs.StringVar(&f.failOn, "fail-on", "", "exit 3 when a finding STANDS at this tier after the run: `reproduced` (its script ran and exited 0, and no reproduced refutation or adjudication overturned it). Off by default — the record is written either way; this is the merge gate's switch, for CI")
	fs.DurationVar(&f.timeout, "timeout", time.Minute, "wall-clock bound on each reproduction script")
	fs.StringVar(&f.substrate, "substrate", substrateJail, "where the reproduction scripts run: `jail` (default) — the offline sandbox certify grades in, over the tree at the commit with your dependency directories read-only; or `workspace` — a detached git worktree on the host, network on, model keys removed from the environment, for where the caller is the isolation boundary (a CI runner, as with certify --repo --substrate workspace)")
	fs.IntVar(&f.maxBytes, "max-bytes", 200000, "how many bytes of the scope the reviewer is shown; files past the cap are listed by name and the review records them as unshown")
	return fs, f
}

func runReviewRun(args []string, stdout, stderr io.Writer) int {
	fs, f := reviewFlagSet(stderr)
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	repoDir, scope, model, verifier, ledgerFlag, attest, push := &f.repoDir, &f.scope, &f.model, &f.verifier, &f.ledger, &f.attest, &f.push
	failOn := strings.ToLower(strings.TrimSpace(f.failOn))
	if failOn != "" && failOn != "reproduced" {
		fmt.Fprintf(stderr, "corral review: --fail-on %q is not a tier — the gate fires on `reproduced` (or is off when unset)\n", f.failOn)
		return 2
	}
	noLedger, timeout, maxBytes := &f.noLedger, &f.timeout, &f.maxBytes
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "corral review: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if strings.TrimSpace(*scope) == "" || strings.TrimSpace(*model) == "" {
		fmt.Fprintln(stderr, "corral review: --scope and --reviewer-model are required")
		return 2
	}
	root, err := filepath.Abs(*repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", err)
		return 2
	}
	commit := gitHeadCommit(root)
	if commit == "" {
		fmt.Fprintln(stderr, "corral review: --repo is not a git checkout at a commit — a review names the revision it reviewed")
		return 2
	}
	if _, err := resolveSeatRegistry("corral review", root, []seatFlag{{flag: "reviewer-model", val: model}, {flag: "verifier-model", val: verifier}}, stderr); err != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", err)
		return 2
	}
	// The decorrelation rule, the same one the writer and the critic live
	// under: a verifier that is the reviewer's own model would grade its
	// own work. Checked before anything is spent.
	if strings.TrimSpace(*verifier) != "" && agentbackend.SameSeat(*verifier, *model) {
		fmt.Fprintf(stderr, "corral review: --verifier-model %s resolves to the reviewer's own model %s — the verifier must be a different one (nemo iudex in causa sua)\n", *verifier, agentbackend.SeatModelOf(*model))
		return 2
	}
	// The sandbox before anything is spent: every reproduction runs in it,
	// and with none, nothing a seat says could be checked.
	iso, jerr := resolveReviewSubstrate(f.substrate)
	if jerr != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", jerr)
		return 2
	}
	// The substrate's seed — a worktree, the tree copy, vendoring, the text
	// cap — is built now, before any seat is paid: one that fails costs
	// nothing. The seats read their own, separate worktree, so building it
	// first does not let a seat arrange the tree its scripts are judged in.
	// The signal-aware context unwinds a Ctrl-C through every cleanup.
	ctx, stop := auditContext(stderr)
	defer stop()
	rep, cleanup, rerr := newReproducerFn(f.substrate, iso, root, commit, *scope, *timeout, stderr)
	if rerr != nil {
		fmt.Fprintf(stderr, "corral review: preparing where the reproductions run: %v\n", rerr)
		return 1
	}
	defer cleanup()
	// The worktrees first: one for an agentic seat to read (it is never
	// handed the checkout), and — after the seats have spoken — the one
	// the reproductions run in. Two, so a seat that could write could not
	// pre-arrange the tree its own scripts are judged in.
	seatTree, seatCleanup, terr := newWorktree(root, commit)
	if terr != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", terr)
		return 1
	}
	defer seatCleanup()
	agentTimeout := 30 * time.Minute
	seat := func(spec string) (agentbackend.Backend, string, error) {
		if _, _, isAgent := agentbackend.AgentSeat(spec); isAgent {
			b, err := agentbackend.ForSeat(spec, agentbackend.SeatOptions{Dir: seatTree, Timeout: agentTimeout, Tools: reviewSeatTools})
			if err != nil {
				return nil, "", err
			}
			// The same helper certify's Verdict.SeatTools is built with.
			return b, agentbackend.SeatTool(spec), nil
		}
		b, err := newReviewerBackend(spec, "")
		return b, "", err
	}
	backend, reviewerTool, err := seat(*model)
	if err != nil {
		fmt.Fprintf(stderr, "corral review: reviewer seat: %v\n", err)
		return 2
	}
	var verifierBackend agentbackend.Backend
	verifierTool := ""
	if strings.TrimSpace(*verifier) != "" {
		if verifierBackend, verifierTool, err = seat(*verifier); err != nil {
			fmt.Fprintf(stderr, "corral review: verifier seat: %v\n", err)
			return 2
		}
	}
	_, _, reviewerIsAgent := agentbackend.AgentSeat(*model)
	_, _, verifierIsAgent := agentbackend.AgentSeat(*verifier)

	// Read from seatTree, the detached worktree AT commit, never from root:
	// the entry is recorded against commit and the reproductions run on it,
	// so an API seat shown the working copy would review uncommitted code
	// (modified or untracked) under HEAD's name, and its findings would then
	// be graded against different bytes. An agentic seat already reads
	// seatTree; this makes the API seat and the verifier's brief agree.
	sc, err := review.LoadScope(seatTree, *scope, *maxBytes)
	if err != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", err)
		return 2
	}
	repoName := resolveRepoName(root, "")
	allFiles := append(append([]string{}, sc.Files...), sc.Unshown...)
	if reviewerIsAgent {
		fmt.Fprintf(stdout, "review — %s @ %.12s, scope %s: %d file(s), read by the seat itself in a disposable copy (no byte cap)", repoName, commit, *scope, len(allFiles))
	} else {
		fmt.Fprintf(stdout, "review — %s @ %.12s, scope %s: %d file(s), %d bytes shown", repoName, commit, *scope, len(sc.Files), sc.Bytes)
		if sc.Truncated {
			fmt.Fprintf(stdout, ", %d NOT shown (--max-bytes)", len(sc.Unshown))
		}
	}
	party := auditpush.CommitIdentity(root, commit)
	if line := partyLine(party); line != "" {
		fmt.Fprintf(stdout, "\n  %s", line)
	}
	fmt.Fprintf(stdout, "\n  reviewer: %s%s (cold — it has never seen this repository)\n", *model, toolNote(reviewerTool))
	if verifierBackend != nil {
		fmt.Fprintf(stdout, "  verifier: %s%s (adversarial to the reviewer; a different model by rule)\n", *verifier, toolNote(verifierTool))
	}
	// Say it NOW, not when someone queries the record months later and finds
	// a tool name where a model belongs. An unpinned agentic seat lets the
	// CLI pick its own model and never tells us which, so the row cannot
	// carry a per-model comparison — and that is unrecoverable afterwards.
	var unpinned []string
	if !agentbackend.SeatNamesAModel(*model) {
		unpinned = append(unpinned, "--reviewer-model "+*model)
	}
	if verifierBackend != nil && !agentbackend.SeatNamesAModel(*verifier) {
		unpinned = append(unpinned, "--verifier-model "+*verifier)
	}
	if len(unpinned) > 0 {
		fmt.Fprintf(stdout, "  note: %s name(s) a TOOL, not a model — the CLI picks its own and does not report it,\n"+
			"        so this entry is recorded model-unresolved and cannot feed a per-model comparison.\n"+
			"        Pin it (%s:<model>) if this round is meant to be data.\n",
			strings.Join(unpinned, " and "), strings.SplitN(*model, ":", 2)[0])
	}

	r := review.Review{Repo: repoName, Commit: commit, Author: party.Author, Committer: party.Committer, CoAuthors: party.CoAuthors,
		Scope: *scope, ReviewerModel: *model, ReviewerTool: reviewerTool, Lang: langOfScope(allFiles),
		ReviewerModelResolved: agentbackend.SeatNamesAModel(*model),
		Substrate:             substrateOf(rep), StartedAt: time.Now().UTC(),
		FilesShown: sc.Files, BytesShown: sc.Bytes, Truncated: sc.Truncated}
	userTurn := review.Brief(repoName, commit, *scope, sc)
	if reviewerIsAgent {
		userTurn = review.AgentBrief(repoName, commit, *scope, allFiles)
		r.FilesShown, r.BytesShown, r.Truncated = allFiles, 0, false
	}
	reply, err := backend.Chat([]agentbackend.Message{
		{Role: "system", Content: review.BriefSystem},
		{Role: "user", Content: userTurn},
	}, nil)
	if operatorInterrupted(err) {
		return refuseInterrupted(stderr, "corral review")
	}
	if err != nil {
		fmt.Fprintf(stderr, "corral review: the reviewer seat failed: %v\n", err)
		return 1
	}
	addReviewUsage(&r, reply.Usage)
	opinion, findings, sound, perr := review.Parse(reply.Content)
	if perr != nil {
		fmt.Fprintf(stderr, "corral review: %v\n--- the reply, verbatim ---\n%s\n", perr, reply.Content)
		return 1
	}
	r.Opinion, r.Findings, r.Sound = opinion, findings, sound

	// The reproductions: every REPRODUCED script, in the substrate built
	// above, so neither the operator's checkout nor the host is the subject.
	review.Reproduce(ctx, rep, &r)
	r.Coverage = review.CoverageNote(r)

	// The verifier sees the review AS RECORDED — reproductions run, tiers
	// demoted — and the same scope, and its own reproductions run in the
	// same worktree.
	if verifierBackend != nil {
		vturn := review.VerifierBrief(r, sc)
		if verifierIsAgent {
			vturn = review.VerifierAgentBrief(r, allFiles)
		}
		vreply, verr := verifierBackend.Chat([]agentbackend.Message{
			{Role: "system", Content: review.VerifierBriefSystem},
			{Role: "user", Content: vturn},
		}, nil)
		if operatorInterrupted(verr) {
			// Not "recorded unverified": the operator stopped the run, and
			// a signed entry (and a push) for it would record a review
			// nobody finished.
			return refuseInterrupted(stderr, "corral review")
		}
		if verr == nil {
			// The call answered, so it was made and spent, whether or not
			// its reply parses below: an unverified review must not read
			// cheaper than it was.
			addReviewUsage(&r, vreply.Usage)
		}
		if verr != nil {
			fmt.Fprintf(stderr, "corral review: the verifier seat failed: %v — the review is recorded unverified\n", verr)
		} else if vopinion, refs, perr := review.ParseRefutations(vreply.Content, *verifier); perr != nil {
			fmt.Fprintf(stderr, "corral review: %v — the review is recorded unverified\n--- the reply, verbatim ---\n%s\n", perr, vreply.Content)
		} else {
			// A verifier that answered with no verdict on any finding has
			// not verified anything, and the record must not read as if
			// it had: its reply is kept, marked, and every finding is
			// graded as unverified.
			if len(refs) == 0 && len(r.Findings) > 0 {
				vopinion = "(the verifier returned no verdicts on any finding; its reply, verbatim: " + clip.Tail(vreply.Content, 2000) + ")"
				fmt.Fprintf(stderr, "corral review: the verifier %s returned no verdict on any of %d finding(s) — the review is recorded with its reply, unverified\n", *verifier, len(r.Findings))
			}
			r.VerifierTool = verifierTool
			r.VerifierModelResolved = agentbackend.SeatNamesAModel(*verifier)
			review.Verify(ctx, rep, &r, *verifier, vopinion, refs)
		}
	}

	// A signal can land after the last seat answered; the writes below would
	// then record a review the operator stopped.
	if operatorInterrupted(nil) {
		return refuseInterrupted(stderr, "corral review")
	}
	printReview(stdout, r, nil)

	// The statement first: it signs the reproductions; the entry, written
	// after, names it. Fail-open like every write after a measurement.
	// statementFiles are what it wrote, removed if an interrupt lands before
	// the entry, so "nothing recorded" stays true.
	var statementFiles []string
	if p := strings.TrimSpace(*attest); p != "" {
		sha, envPath, signErr, aerr := writeReviewStatement(p, r)
		if aerr != nil {
			fmt.Fprintf(stderr, "corral review: writing the statement to %s: %v\n", p, aerr)
		} else {
			statementFiles = append(statementFiles, p)
			if signErr == nil {
				statementFiles = append(statementFiles, envPath)
			}
			r.StatementSHA256 = sha
			// "signed" is said only for an envelope THIS run wrote. It used
			// to be decided by stat-ing the envelope path, so a leftover
			// envelope from an earlier run announced a signature nobody
			// produced (review 8377ae6320cc#R6).
			signed := "signed into " + envPath
			if signErr != nil {
				signed = "unsigned: " + signErr.Error()
			}
			fmt.Fprintf(stdout, "\nattestation: %s (sha256 %.12s…, %s) — the reproductions signed, the opinion bound by its hash\n", p, sha, signed)
		}
	}

	// The gate, decided from the record the run produced: a finding that
	// reproduced and was not overturned stands. The entry is written and
	// the push happens whatever the gate says — a verdict you only keep
	// when it is flattering is not evidence.
	exit := 0
	if failOn == "reproduced" {
		for _, fd := range r.Findings {
			if o := review.OutcomeOf(fd, nil); o.Known && o.Held {
				exit = reviewExitStanding
				break
			}
		}
		if exit != 0 {
			fmt.Fprintf(stdout, "\ngate: a REPRODUCED finding stands — exit %d (--fail-on reproduced)\n", exit)
		}
	}
	if *noLedger {
		return exit
	}
	ledgerDir := strings.TrimRight(*ledgerFlag, "/")
	if ledgerDir == "" {
		ledgerDir = defaultLedgerDir(root)
	}
	signer, signed, kerr := ledgerSignerFromLocalKey()
	if kerr != nil {
		fmt.Fprintf(stderr, "corral review: no entry written to %s: %v\n", ledgerDir, kerr)
		return 1
	}
	if operatorInterrupted(nil) {
		return refuseInterruptedAfter(stderr, "corral review", statementFiles)
	}
	name, werr := auditpush.WriteReview(ledgerDir, r, signer)
	if werr != nil {
		fmt.Fprintf(stderr, "corral review: writing the entry to %s: %v\n", ledgerDir, werr)
		return exit
	}
	entries, _ := auditpush.ReadLedgerDir(ledgerDir)
	var last auditpush.LedgerEntry
	if n := len(entries); n > 0 {
		last = entries[n-1]
	}
	fmt.Fprintf(stdout, "\nledger: review entry %s written to %s (%s)\n  adjudicate a finding: corral review adjudicate %s %.12s#R1 --confirm|--refute --reason \"…\"\n", name, ledgerDir, signed, ledgerDir, last.Hash)
	// The push, last and fail-open, like certify's: the same entry's rows
	// into a warehouse, keyed by the entry's hash.
	if p := strings.TrimSpace(*push); p != "" && last.Kind == auditpush.KindReview {
		if operatorInterrupted(nil) {
			// The entry above is written; the push is not, and the run says
			// exactly that — never "nothing recorded".
			fmt.Fprintf(stderr, "corral review: interrupted — the entry %s was written to %s, and it was NOT pushed to %s\n", name, ledgerDir, p)
			return exitInterrupted
		}
		if c, perr := auditpush.PushReviewEntry(p, last, f.pushSource); perr != nil {
			fmt.Fprintf(stderr, "corral review: pushing to %s: %v\n", p, perr)
		} else {
			fmt.Fprintf(stdout, "  pushed %d review, %d finding row(s) to %s\n", c.Reviews, c.Findings, p)
		}
	}
	return exit
}

// newReviewerBackend is the seat's constructor; a test substitutes a fake.
var newReviewerBackend = seatBackend

// seatBackend resolves one seat's model to a backend the way the goal
// deriver does: a local daemon when the registry placed it on one, the
// pinned MODEL_BACKEND when it serves this vendor, else the model's own
// provider.
func seatBackend(model, endpoint string) (agentbackend.Backend, error) {
	if endpoint != "" {
		return agentbackend.NewOllamaBackend(endpoint, model), nil
	}
	if v := agentbackend.VendorOf(model); v != "" && !isAgentSeat(model) && backendPinned() && (baseVendor() == "" || baseVendor() == v) {
		base := agentbackend.FromEnv()
		if sw, ok := base.(agentbackend.ModelSwitcher); ok {
			return sw.WithModel(model), nil
		}
		return base, nil
	}
	// ForSeat defers to ForModelOrLocal for a non-agent name, so the local
	// fallback is kept; an agent spec never reaches here (seat() takes it).
	return agentbackend.ForSeat(model, agentbackend.SeatOptions{})
}

// reviewSeatTools are the tools an agent seat in review is granted: it reads
// the tree, so it may read, search and list it — and nothing else. A
// certify seat is granted none; its prompt carries the code.
var reviewSeatTools = []string{"Read", "Grep", "Glob"}

// jailReproducer runs each reproduction script in the same offline jail
// certify grades in. A reproduction is a model's shell script: before this it
// ran in a detached worktree on the host, with the network on and only
// credentials scrubbed from its environment, which made review the one verb
// that ran model-written code outside the sandbox. Each script gets a fresh
// workspace seeded from the tree at the commit (so one script cannot arrange
// the next one's tree), the operator's dependency directories bound
// read-only, and no network. What the jail cannot carry is stated, not
// worked around: .git is not in the seed, nor are binaries outside testdata,
// so a script that needs git history or the network fails and is demoted.
type jailReproducer struct {
	enum  detailedEnumerator
	files map[string]string
	note  string
}

// Substrate is what the review entry records about where its reproductions
// ran, including which dependency directories were actually there.
func (j jailReproducer) Substrate() string { return j.note }

// detailedEnumerator is the jail's run that keeps the exit code and stderr,
// which a reproduction's outcome is read from.
type detailedEnumerator interface {
	EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error)
}

// newWorktree is a detached git worktree at commit under a temp dir, and
// the function that removes it.
func newWorktree(root, commit string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "corral-review-")
	if err != nil {
		return "", nil, err
	}
	tree := filepath.Join(dir, "tree")
	// #nosec G204 -- fixed argv; root and commit are the operator's own checkout and its HEAD
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", tree, commit).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("git worktree at %.12s: %v: %s", commit, err, strings.TrimSpace(string(out)))
	}
	return tree, func() {
		_ = exec.Command("git", "-C", root, "worktree", "remove", "--force", tree).Run() // #nosec G204 -- fixed argv
		os.RemoveAll(dir)
	}, nil
}

// newJailReproducer seeds the jail for reproductions at commit: a worktree
// at the commit, vendored for the scope's language as certify vendors (inside
// the jail, never on the host), plus the dependency directories of the
// operator's checkout at root, which a worktree does not have.
func newJailReproducer(iso sandbox.Isolator, root, commit, scope string, timeout time.Duration, progress io.Writer) (review.Reproducer, func(), error) {
	tree, removeTree, err := newWorktree(root, commit)
	if err != nil {
		return nil, nil, err
	}
	langName := reviewLang(tree, scope)
	seed, err := buildRepoSeed("corral review", tree, langName, iso, nil, false, progress)
	if err != nil {
		removeTree()
		return nil, nil, err
	}
	cleanup := func() { seed.cleanup(); removeTree() }
	_, deps, err := loadRepoFiles(root, loadOpts{BackendName: iso.Name(), NoFiles: true})
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("finding the dependency directories in %s: %w", root, err)
	}
	// The operator's installed dependency dirs first: where a repository
	// commits part of one (vendor/, a slice of node_modules), the installed
	// tree is the complete one.
	binds := deps
	for _, b := range seed.binds {
		if !slices.ContainsFunc(binds, func(d adequacy.DepBind) bool { return d.Rel == b.Rel }) {
			binds = append(binds, b)
		}
	}
	// A dependency directory this backend cannot relocate into the jail
	// (sandbox-exec; a dir the container's user cannot read) is neither
	// bound nor copied: said on stderr and on the record, never implied.
	var bound, unbound []string
	_, every, _ := loadRepoFiles(root, loadOpts{BackendName: "bwrap", NoFiles: true}) // every dependency dir, bindable or not
	for _, d := range every {
		if slices.ContainsFunc(binds, func(b adequacy.DepBind) bool { return b.Rel == d.Rel }) {
			bound = append(bound, d.Rel)
		} else {
			unbound = append(unbound, d.Rel)
		}
	}
	if len(unbound) > 0 {
		fmt.Fprintf(progress, "corral review: the %s sandbox cannot mount %s; a reproduction that needs it will fail and be demoted\n", iso.Name(), strings.Join(unbound, ", "))
	}
	// The toolchain bound is the scope language's own test command's, never
	// one read from a script: a model's words must not choose host
	// directories to mount (adequacy.WithToolchainFrom).
	var toolchain []string
	if p, ok := lang.ByName(langName); ok {
		toolchain = p.TestCmd()
	}
	enum, ok := newRunEnumerator(iso, timeout, binds, adequacy.WithToolchainFrom(toolchain), adequacy.WithMaxOutput(64<<10)).(detailedEnumerator)
	if !ok {
		cleanup()
		return nil, nil, errors.New("the jail cannot report a script's exit code")
	}
	note := "jail (" + iso.Name() + ", offline: the tree at the commit"
	if len(bound) > 0 {
		note += "; " + strings.Join(bound, ", ") + " bound read-only"
	}
	if len(unbound) > 0 {
		note += "; not mounted: " + strings.Join(unbound, ", ")
	}
	return jailReproducer{enum: enum, files: seed.files, note: note + ")"}, cleanup, nil
}

// resolveReviewSubstrate checks --substrate and, for the jail, resolves the
// sandbox, before anything is spent: with none, nothing a seat says could be
// checked. The workspace substrate needs none.
func resolveReviewSubstrate(name string) (sandbox.Isolator, error) {
	switch name {
	case substrateJail:
		iso, err := resolveScanJail()
		if err != nil {
			return nil, fmt.Errorf("reproductions run in a sandbox (--substrate jail), and there is none here: %w\n(on a CI runner, which is itself the isolation boundary, --substrate workspace runs them in a worktree on the host)", err)
		}
		return iso, nil
	case substrateWorkspace:
		return nil, nil
	}
	return nil, fmt.Errorf("--substrate %q: one of %s (default) or %s", name, substrateJail, substrateWorkspace)
}

// newReproducer is the reproduction substrate --substrate named, for review
// and review recheck alike.
func newReproducer(name string, iso sandbox.Isolator, root, commit, scope string, timeout time.Duration, progress io.Writer) (review.Reproducer, func(), error) {
	if name == substrateWorkspace {
		tree, cleanup, err := newWorktree(root, commit)
		if err != nil {
			return nil, nil, err
		}
		return worktreeReproducer{runner: adequacy.NewWorkspaceRunner(tree, timeout, adequacy.WithWorkspaceMaxOutput(64<<10))}, cleanup, nil
	}
	return newJailReproducer(iso, root, commit, scope, timeout, progress)
}

// substrateOf is what the review entry records about where its
// reproductions ran.
func substrateOf(rep review.Reproducer) string {
	if s, ok := rep.(interface{ Substrate() string }); ok {
		return s.Substrate()
	}
	return "workspace (a detached worktree at the commit; not a jail)"
}

// newReproducerFn is newReproducer, a seam so a test can fail the seed and
// show that no seat was paid first.
var newReproducerFn = newReproducer

// worktreeReproducer runs a script in a detached git worktree on the host:
// --substrate workspace, where the caller is the isolation boundary.
type worktreeReproducer struct {
	runner *adequacy.WorkspaceRunner
}

func (w worktreeReproducer) Run(ctx context.Context, script string) (string, int, error) {
	res, err := w.runner.EnumerateDetailed(ctx, nil, []string{"sh", "-c", script})
	out := res.Output
	if res.Stderr != "" {
		out += "\n[stderr]\n" + res.Stderr
	}
	return out, res.ExitCode, err
}

// reviewLang is the language most of the scope's files are written in, for
// the one vendoring step a jail seed takes; "" when none is recognised.
func reviewLang(tree, scope string) string {
	count := map[string]int{}
	_ = filepath.WalkDir(filepath.Join(tree, scope), func(path string, d iofs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if p, ok := lang.Detect(path); ok {
			count[p.Name()]++
		}
		return nil
	})
	best := ""
	for _, n := range slices.Sorted(maps.Keys(count)) {
		if count[n] > count[best] {
			best = n
		}
	}
	return best
}

// addReviewUsage folds one seat's reply into the review's record: counted
// always, its tokens summed only when its wire reported them. review has no
// --max-tokens, so an unmetered seat is not refused here; it is recorded as
// not metered instead of as a call that cost nothing.
func addReviewUsage(r *review.Review, u agentbackend.Usage) {
	r.Calls++
	if u.Unmetered {
		r.UnmeteredCalls++
		return
	}
	r.InputTokens += int64(u.InputTokens)
	r.OutputTokens += int64(u.OutputTokens)
}

// toolNote renders an agentic seat's tool version beside its name. certify's
// models line and its verdict cache key (seatToolProbe.keyName, via
// modelSetKeyWith) spell a seat the same
// way.
func toolNote(v string) string {
	if v == "" {
		return ""
	}
	return " [" + v + "]"
}

func (j jailReproducer) Run(ctx context.Context, script string) (string, int, error) {
	res, err := j.enum.EnumerateDetailed(ctx, j.files, []string{"sh", "-c", script})
	out := res.Output
	if res.Stderr != "" {
		out += "\n[stderr]\n" + res.Stderr
	}
	return out, res.ExitCode, err
}

// printReview renders a review, with adjudications (by finding ref) when
// the caller has them.
// partyLine names the audited party as the record will: who made the
// commit under review. "" when the checkout could not say.
func partyLine(id auditpush.Identity) string {
	if id.IsZero() {
		return ""
	}
	s := "change by " + id.Author
	if id.Committer != "" && id.Committer != id.Author {
		s += ", committed by " + id.Committer
	}
	if co := id.CoAuthorList(); len(co) > 0 {
		s += ", with " + strings.Join(co, ", ")
	}
	return s
}

func printReview(w io.Writer, r review.Review, adj map[string]auditpush.Adjudication) {
	fmt.Fprintf(w, "\n%s\n", r.Opinion)
	rep, cr, hy := r.Counts()
	fmt.Fprintf(w, "\nfindings: %d reproduced, %d code-read, %d hypothesis\n", rep, cr, hy)
	if len(r.Findings) > 0 {
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tTIER\tSEVERITY\tWHERE\tCLAIM\tVERIFIER · HUMAN\t")
		for _, f := range r.Findings {
			tier := f.Tier
			if f.Tier != f.Declared {
				tier = fmt.Sprintf("%s (declared %s)", f.Tier, f.Declared)
			}
			where := f.File
			if f.Line > 0 {
				where = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			verdict := ""
			if x := f.Refutation; x != nil && x.Verdict == review.VerdictRefuted {
				verdict = "refuted (" + strings.ToLower(x.Tier) + ") by " + x.Model
			} else if x != nil {
				verdict = "stands (" + x.Model + ")"
			}
			if a, ok := adj[f.ID]; ok {
				verdict = strings.TrimSpace(verdict + " · " + a.Verdict + " by " + a.By)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t\n", f.ID, tier, f.Severity, where, f.Claim, verdict)
		}
		tw.Flush()
		for _, f := range r.Findings {
			if f.Script == "" {
				continue
			}
			fmt.Fprintf(w, "\n%s — script:\n%s\n", f.ID, indent(f.Script))
			if f.ExitCode != nil {
				fmt.Fprintf(w, "%s — exit %d, output:\n%s\n", f.ID, *f.ExitCode, indent(strings.TrimSpace(f.Stdout)))
			}
			switch {
			case f.Unrun != "":
				// The harness failed: no outcome, nobody graded — said as such,
				// never as a demotion.
				fmt.Fprintf(w, "%s — NOT RUN (harness): %s — no outcome; grades nobody\n", f.ID, f.Unrun)
			case f.Demoted != "":
				fmt.Fprintf(w, "%s — DEMOTED to %s: %s\n", f.ID, f.Tier, f.Demoted)
			}
			if a, ok := adj[f.ID]; ok {
				fmt.Fprintf(w, "%s — %s by %s: %s\n", f.ID, a.Verdict, a.By, a.Reason)
			}
		}
	}
	if r.VerifierModel != "" {
		fmt.Fprintf(w, "\nverifier %s:\n%s\n", r.VerifierModel, r.VerifierOpinion)
		for _, f := range r.Findings {
			x := f.Refutation
			if x == nil {
				continue
			}
			switch {
			case x.Verdict == review.VerdictStands:
				fmt.Fprintf(w, "\n%s — STANDS: %s\n", f.ID, x.Argument)
			default:
				tier := x.Tier
				if x.Tier != x.Declared {
					tier = fmt.Sprintf("%s (declared %s)", x.Tier, x.Declared)
				}
				fmt.Fprintf(w, "\n%s — REFUTED [%s]: %s\n", f.ID, tier, x.Argument)
				if x.Script != "" {
					fmt.Fprintf(w, "%s — refutation script:\n%s\n", f.ID, indent(x.Script))
				}
				if x.ExitCode != nil {
					fmt.Fprintf(w, "%s — refutation exit %d, output:\n%s\n", f.ID, *x.ExitCode, indent(strings.TrimSpace(x.Stdout)))
				}
				switch {
				case x.Unrun != "":
					fmt.Fprintf(w, "%s — refutation NOT RUN (harness): %s — moves nothing\n", f.ID, x.Unrun)
				case x.Demoted != "":
					fmt.Fprintf(w, "%s — refutation DEMOTED to %s: %s\n", f.ID, x.Tier, x.Demoted)
				}
			}
		}
		if r.VerifierNote != "" {
			fmt.Fprintf(w, "\nverifier reply, not used: %s\n", r.VerifierNote)
		}
	}
	fmt.Fprintln(w, "\nchecked and found sound:")
	if len(r.Sound) == 0 {
		fmt.Fprintln(w, "  (the reviewer listed nothing — the review's coverage is unknown)")
	}
	for _, s := range r.Sound {
		fmt.Fprintf(w, "  · %s\n", s)
	}
	if r.Truncated {
		fmt.Fprintln(w, "\nsome of the scope was NOT shown to the reviewer (--max-bytes) — nothing above is a claim about those files")
	}
	if r.Coverage != "" {
		fmt.Fprintf(w, "\ncoverage %s\n", r.Coverage)
	}
}

// writeReviewStatement writes the plain statement to path and returns its
// sha256, the envelope path THIS call wrote (or "" with signErr saying why
// no envelope was produced — no key, a signing failure), and any error
// writing the plain file. A stale envelope beside path is removed first:
// an envelope that does not belong to this statement must not survive it.
func writeReviewStatement(path string, r review.Review) (sha, envPath string, signErr, err error) {
	return writeStatement(path, certify.BuildReviewAttestation(r))
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func runReviewShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "corral review show: usage: corral review show <ledger dir> <review hash>")
		// A help request is answered, not refused (see flagParseExit).
		if wantsHelp(args) {
			return 0
		}
		return 2
	}
	dir := strings.TrimRight(args[0], "/")
	rec, err := readLedgerRecord(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review show: %v\n", err)
		return 1
	}
	// Found among ALL entries — a person may need to read a withdrawn
	// review — but a withdrawn one is announced before anything else,
	// and its adjudications went with it.
	e, err := auditpush.FindReview(rec.All, args[1])
	if err != nil {
		fmt.Fprintf(stderr, "corral review show: %v\n", err)
		return 1
	}
	if ret, gone := rec.retractionOf(e); gone {
		fmt.Fprintf(stdout, "RETRACTED %s by entry %.12s: %s\n(this review does not stand; the record below is what was withdrawn)\n\n", ret.Pushed.UTC().Format("2006-01-02 15:04"), ret.Hash, ret.Reason)
	}
	adj := map[string]auditpush.Adjudication{}
	for ref, a := range rec.liveAdjudications() {
		if h, id, ok := strings.Cut(ref, "#"); ok && h == e.Hash {
			adj[id] = a
		}
	}
	r := *e.Review
	fmt.Fprintf(stdout, "review %.12s — %s @ %.12s, scope %s, by %s, %s\n", e.Hash, r.Repo, r.Commit, r.Scope, r.ReviewerModel, e.Pushed.UTC().Format("2006-01-02 15:04"))
	printReview(stdout, r, adj)
	return 0
}

func runReviewAdjudicate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral review adjudicate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	confirm := fs.Bool("confirm", false, "the finding is real as stated")
	refute := fs.Bool("refute", false, "the finding is not real, or not as stated")
	reason := fs.String("reason", "", "why, in your words (required)")
	by := fs.String("by", "", "who is deciding (default: the OS user)")
	push := fs.String("push", "", "also append the verdict as a corral_adjudications row to this warehouse (a DuckDB path, or md:<db>), joined to the review's rows by the entry's hash")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 2 || *confirm == *refute || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "corral review adjudicate: usage: corral review adjudicate <ledger dir> <review hash>#<Rn> --confirm|--refute --reason \"…\" [--by <who>]")
		return 2
	}
	verdict := auditpush.VerdictConfirmed
	if *refute {
		verdict = auditpush.VerdictRefuted
	}
	who := strings.TrimSpace(*by)
	if who == "" {
		if u, err := user.Current(); err == nil {
			who = u.Username
		}
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	signer, signed, kerr := ledgerSignerFromLocalKey()
	if kerr != nil {
		fmt.Fprintf(stderr, "corral review adjudicate: %v\n", kerr)
		return 1
	}
	name, err := auditpush.WriteAdjudication(dir, fs.Arg(1), verdict, who, *reason, signer)
	if err != nil {
		fmt.Fprintf(stderr, "corral review adjudicate: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s by %s — %s (%s, entry %s)\n", fs.Arg(1), verdict, who, *reason, signed, name)
	if p := strings.TrimSpace(*push); p != "" {
		entries, _ := auditpush.ReadLedgerDir(dir)
		if n := len(entries); n > 0 {
			if _, perr := auditpush.PushReviewEntry(p, entries[n-1], false); perr != nil {
				fmt.Fprintf(stderr, "corral review adjudicate: pushing to %s: %v\n", p, perr)
			} else {
				fmt.Fprintf(stdout, "  pushed the verdict to %s\n", p)
			}
		}
	}
	return 0
}
