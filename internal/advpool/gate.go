// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testgen"
	"github.com/pdbethke/corralai/internal/transparency"
)

// pluginFor resolves the language plugin from the code file's extension,
// fail-closed on an unknown language (the gate never grades what it cannot
// run).
func pluginFor(codePath string) (golang.Plugin, error) {
	p, ok := golang.Detect(codePath)
	if !ok {
		return nil, fmt.Errorf("advpool: no language plugin for %q — refusing to grade", codePath)
	}
	return p, nil
}

// advPoolTestPath derives the synthetic test-file name a candidate test is
// written to in the jail workspace from the code file's own path via the
// resolved language plugin's own convention. Falls back to the legacy go
// convention (same base name, `_test.go` suffix, same directory) when no
// plugin resolves — kept identical to the prior implementation so an
// unresolvable path still behaves exactly as before.
func advPoolTestPath(codePath string) string {
	if p, err := pluginFor(codePath); err == nil {
		return p.TestPaths(codePath)[0].Path
	}
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + "_test.go"
	}
	return filepath.Join(dir, filepath.Base(base)+"_test.go")
}

// goScaffold is the exact go workspace scaffold/default test command kept
// from the prior implementation's fallback (mirrors
// internal/controlgate.LangScaffold("go"), duplicated here in miniature so
// this leaf package need not import internal/controlgate).
func goScaffold() (base map[string]string, testCmd []string) {
	return map[string]string{"go.mod": "module control\ngo 1.26\n"}, []string{"go", "test", "./..."}
}

// advPoolBase returns the workspace scaffold + default test command for the
// code file's resolved language plugin. The default testCmd is deliberately
// RECURSIVE for go ("./...", never controlgate.LangScaffold's own "./"
// default): a run's code_path is very commonly a subdirectory (e.g.
// internal/auth/login.go), which lands the candidate files under
// internal/auth/ in the jail workspace — "go test ./" from the module root
// only ever sees the root package (no .go files there), so a non-recursive
// default would silently no-op the scorer/compile-check for every
// subdirectory target. This is the SAME asymmetry bug CompileTest had (I-1):
// the scorer already honors the run's own TestCmd when set, this only fixes
// the fallback used when TestCmd is empty.
//
// Unknown/unresolvable codePath falls back to the exact go scaffold/cmd kept
// from the prior go-only implementation — callers that can fail instead
// (StartRun) preflight first and refuse the run rather than silently
// grading under the wrong language.
func advPoolBase(codePath string) (base map[string]string, testCmd []string) {
	if p, err := pluginFor(codePath); err == nil {
		return p.Scaffold(), p.TestCmd()
	}
	return goScaffold()
}

// JailScorer adapts adequacy.Score (the SAME deterministic, brain-side,
// jail-run mutation scorer the control gate uses) to advpool.Scorer. This is
// the soundness-#1 seam: the driver never trusts a worker's self-reported
// kill rate, only what this Scorer actually observes running in the jail.
type JailScorer struct {
	Jail adequacy.Jail
	// BaseFiles, when non-nil, switches the scorer into REPO-AWARE mode: the
	// jail workspace is seeded with these files (a whole cloned repo/package,
	// keyed by repo-relative path) instead of the synthetic single-file
	// scaffold, `codePath` is the repo-relative path of the file under audit
	// (so a mutant overwrites the real file IN CONTEXT), and the project's OWN
	// test command (the run's TestCmd) grades it. The dev's tests already live
	// in BaseFiles, so — unlike single-file mode — no synthetic dev-test is
	// overlaid. nil preserves the exact single-file behavior byte-for-byte.
	BaseFiles map[string]string
	// MutantTimeout, when > 0, overrides adequacy.Score's auto-derived
	// per-mutant timeout (see adequacy.WithMutantTimeout) with an explicit
	// cap — the plumbing for `certify --local --test-timeout`. The zero
	// value (so every existing JailScorer{} literal keeps today's behavior
	// unchanged) means auto-derive from the healthy baseline's own runtime.
	MutantTimeout time.Duration
}

func (s JailScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, adequacy.WithMutantTimeout(s.MutantTimeout))
	if err != nil {
		return 0, nil, fmt.Errorf("advpool: score: %w", err)
	}
	return rep.KillRate(), survivorsFrom(rep, mutants), nil
}

// ScoreReport is Score's richer sibling: it reuses the SAME scoreWorkspace +
// adequacy.Score call Score itself makes above, but returns the raw
// adequacy.Report instead of collapsing it to a kill rate + survivor slice —
// so a caller can distinguish a baseline that couldn't pass (CompliantPass
// false) from a genuine zero-kill (CompliantPass true, len(Killed)==0).
func (s JailScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, adequacy.WithMutantTimeout(s.MutantTimeout))
	if err != nil {
		return adequacy.Report{}, fmt.Errorf("advpool: score report: %w", err)
	}
	return rep, nil
}

// ScoreAuthoredReport scores a POOL-authored test (the test-writer's output)
// against mutants — the AuthoredScorer extension tickPoolAdequacy prefers
// over ScoreReport. Identical to ScoreReport in single-file mode (BaseFiles
// nil): scoreWorkspace already overlays `test` at the synthetic path there.
//
// In repo-aware mode (BaseFiles set) it explicitly overlays `test` at
// advPoolTestPath(codePath) — the same path JailValidator.CompileTest already
// overlays it at (see CompileTest above) — instead of silently discarding it
// the way scoreWorkspace does. scoreWorkspace's drop is CORRECT for the DEV
// test (already on disk; overlaying would shadow the real suite — see its own
// doc) but WRONG for an AUTHORED test: it is a brand-new file the repo does
// not already contain, so there is nothing to shadow. Before this method
// existed, tickPoolAdequacy called Score/ScoreReport directly, and in
// repo-aware mode the authored test's content never reached any workspace the
// jail ran — the pool silently re-scored the DEV suite against its own
// already-known survivors, so they "survived" again and ProvenMissed computed
// to 0 for EVERY repo-aware run, unconditionally. The asymmetry was easy to
// miss because CompileTest DOES overlay (so the compile gate passed and
// TestWriterFailed stayed false) while SCORING silently used a workspace that
// never contained the test at all.
func (s JailScorer) ScoreAuthoredReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)
	if s.BaseFiles != nil {
		scoreBase = make(map[string]string, len(s.BaseFiles)+1)
		for k, v := range s.BaseFiles {
			scoreBase[k] = v
		}
		scoreBase[advPoolTestPath(codePath)] = test
	}

	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, adequacy.WithMutantTimeout(s.MutantTimeout))
	if err != nil {
		return adequacy.Report{}, fmt.Errorf("advpool: score authored report: %w", err)
	}
	return rep, nil
}

// scoreWorkspace builds the jail base file-map and the test command for a
// scoring run. In single-file mode (BaseFiles nil) it reproduces the original
// behavior exactly: the language scaffold plus the dev test overlaid at the
// plugin's synthetic test path, defaulting the command to the plugin's when
// the run carries none. In repo-aware mode (BaseFiles set) the whole repo IS
// the base, the dev test already lives inside it (so `test` is NOT overlaid —
// overlaying would shadow the real suite), and the run's own TestCmd (the
// project's command) is authoritative — there is no synthetic default.
func (s JailScorer) scoreWorkspace(codePath, test, testCmd string) (map[string]string, []string) {
	if s.BaseFiles != nil {
		base := make(map[string]string, len(s.BaseFiles))
		for k, v := range s.BaseFiles {
			base[k] = v
		}
		return base, strings.Fields(testCmd)
	}
	base, defaultCmd := advPoolBase(codePath)
	cmd := strings.Fields(testCmd)
	if len(cmd) == 0 {
		cmd = defaultCmd
	}
	scoreBase := make(map[string]string, len(base)+1)
	for k, v := range base {
		scoreBase[k] = v
	}
	scoreBase[advPoolTestPath(codePath)] = test
	return scoreBase, cmd
}

// JailEnumerator implements the driver's TestEnumerator over the SAME
// jail/workspace conventions JailScorer uses (scoreWorkspace), so a matrix
// enumeration sees the identical scaffold/BaseFiles-mode a subsequent
// ScoreReport call for one of its selectors would see. It holds an
// adequacy.Enumerator rather than adequacy.Jail — RunTest only ever answers
// pass/fail, never stdout, so enumeration needs the stdout-capturing sibling
// (see internal/adequacy.Enumerator).
type JailEnumerator struct {
	Jail adequacy.Enumerator
	// BaseFiles mirrors JailScorer.BaseFiles: nil preserves single-file mode.
	BaseFiles map[string]string
}

// Enumerate builds the same jail workspace scoreWorkspace would for a scoring
// run (base scaffold/whole-repo plus the dev test overlaid at its synthetic
// path in single-file mode), overlays codePath -> code exactly as
// adequacy.Score's own run closure does, and runs listCmd through the
// stdout-capturing jail.
func (e JailEnumerator) Enumerate(ctx context.Context, codePath, code, test string, listCmd []string) (string, error) {
	scoreBase, _ := (JailScorer{BaseFiles: e.BaseFiles}).scoreWorkspace(codePath, test, "")
	files := make(map[string]string, len(scoreBase)+1)
	for k, v := range scoreBase {
		files[k] = v
	}
	files[codePath] = code
	return e.Jail.Enumerate(ctx, files, listCmd)
}

// JailValidator brain-side-validates a worker's structured artifacts before
// the driver trusts them: CompileTest jail-compiles a candidate test against
// the code (via `go vet`, which type-checks test files without executing
// them — the "does it compile" check, never "does it pass", which would
// corrupt CompileTest's meaning); ParseMutants is testgen's proven
// mutant-output parser (the Task 1.2 seam), reused verbatim so a distributed
// worker's raw response parses identically to the in-process generator's own
// output.
//
// CompileTest MUST cover the same scope the Scorer actually runs against
// (I-1): a subdirectory code_path (e.g. internal/auth/login.go) lands the
// candidate code+test under internal/auth/ in the jail workspace, so
// `go vet ./` (module root, non-recursive) sees zero .go files there and
// fails EVERY authored test regardless of whether it actually compiles —
// the run then never converges. `go vet ./...` is recursive and always
// covers whatever directory the files actually landed in.
type JailValidator struct {
	Jail adequacy.Jail
	// BaseFiles mirrors JailScorer.BaseFiles: in repo-aware mode the authored
	// test is compile-checked against the WHOLE repo (so a test that imports
	// the package resolves), not the bare single-file scaffold. nil preserves
	// the original single-file behavior.
	BaseFiles map[string]string
}

// CompileError is returned by CompileTest when the authored test builds a
// workspace but does not compile. It carries the FAILING command's own
// compiler output (never a preceding command's — see the note in
// CompileTest's loop) so the driver can feed it back to the test-writer on
// retry instead of blindly re-asking — a bare "does not compile" taught the
// writer nothing, so it repeated the same mistake until it exhausted its
// attempts. Output is empty only when the jail could not surface it (the
// RunTest fallback path).
type CompileError struct {
	Output string
	// Cmd is the argv of the specific command in CompileCheck's sequence
	// that failed (or is empty, for the RunTest fallback / the empty-sequence
	// case), included in Error() so a multi-command sequence (ruby -c /
	// node --check, one file per invocation) says WHICH file's check failed,
	// not just that the sequence as a whole didn't pass.
	Cmd []string
}

func (e *CompileError) Error() string {
	switch {
	case strings.TrimSpace(e.Output) == "" && len(e.Cmd) == 0:
		return "advpool: test does not compile"
	case strings.TrimSpace(e.Output) == "":
		return fmt.Sprintf("advpool: test does not compile (command %v)", e.Cmd)
	default:
		return fmt.Sprintf("advpool: test does not compile (command %v):\n%s", e.Cmd, e.Output)
	}
}

// verboseJail is the optional Jail extension that also returns the compiler
// output. bwrapJail implements it; a Jail that doesn't falls back to the bare
// RunTest path below — still a correct pass/fail, just no output to feed back.
type verboseJail interface {
	RunTestVerbose(ctx context.Context, files map[string]string, cmd []string) (bool, string, error)
}

func (v JailValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	p, err := pluginFor(codePath)
	if err != nil {
		return err
	}
	base := p.Scaffold()
	if v.BaseFiles != nil {
		base = v.BaseFiles
	}
	ws := make(map[string]string, len(base)+2)
	for k, val := range base {
		ws[k] = val
	}
	ws[codePath] = code
	testPath := advPoolTestPath(codePath)
	ws[testPath] = test

	// CompileCheck returns a SEQUENCE of commands (see lang.Plugin.CompileCheck's
	// doc comment): most plugins yield exactly one, but a plugin whose checker
	// can only look at one file per invocation (ruby -c, node --check) yields
	// one per file, meant to run in order and stop at the first failure — the
	// exact "run A, then B only if A succeeded" semantics a shell's `&&` used
	// to express. This runs that sequence directly over v.Jail, WITHOUT ever
	// joining it into a single shell command: v.Jail may be a bare
	// exec.Command on the workspace substrate (internal/adequacy/workspace.go),
	// which has no shell to interpret `&&` at all — see this method's own
	// history (the pallets/flask PYTHONPYCACHEPREFIX regression, and its
	// ruby/js && sibling) for exactly what silently breaks when a plugin's
	// CompileCheck output assumes one.
	seq := p.CompileCheck(codePath, testPath)
	if len(seq) == 0 {
		// An empty sequence must NOT read as "nothing to check, therefore
		// compiles": that is exactly the failure mode this method's own
		// history is about — a validation gate reporting "compiles" without
		// ever invoking a single command. No plugin returns this today (all
		// five yield >=1 command), but a future plugin's early-return, or a
		// stub/fake Jail-adjacent Plugin (e.g. a test double) forgetting a
		// case, must fail CLOSED here, not silently pass every test as
		// compiling. See lang.Plugin.CompileCheck's doc comment for the
		// contract this enforces.
		return fmt.Errorf("advpool: compile-verify test: %s plugin's CompileCheck(%q, %q) returned an empty command sequence — refusing to report a compile pass without running anything", p.Name(), codePath, testPath)
	}
	vj, verbose := v.Jail.(verboseJail)
	for _, cmd := range seq {
		if verbose {
			compiles, output, err := vj.RunTestVerbose(ctx, ws, cmd)
			if err != nil {
				return fmt.Errorf("advpool: compile-verify test: %w", err)
			}
			if !compiles {
				// Output/Cmd carry ONLY this failing command's own output —
				// never a preceding, PASSING command's output. A multi-command
				// sequence (ruby -c code, ruby -c test) means a passing first
				// command would otherwise prefix the real diagnostic with a
				// success message ("Syntax OK\n<real error>"), which is noise
				// the test-writer has to see past on retry, not a fact worth
				// feeding it. Cmd names which command in the sequence failed,
				// since "test does not compile" alone no longer says whether
				// it was the code file or the test file's own check.
				return &CompileError{Output: output, Cmd: cmd}
			}
			continue
		}
		compiles, err := v.Jail.RunTest(ctx, ws, cmd)
		if err != nil {
			return fmt.Errorf("advpool: compile-verify test: %w", err)
		}
		if !compiles {
			return &CompileError{Cmd: cmd}
		}
	}
	return nil
}

func (v JailValidator) ParseMutants(raw, original string) ([]adequacy.Mutant, error) {
	return testgen.ParseMutantsOutput(raw, original)
}

func (v JailValidator) ParseTest(raw string) string {
	return testgen.ParseTestOutput(raw)
}

// CertSigner signs a terminal Verdict via the SAME certify chain
// certifyBuild/report_build uses — mirroring certifyBuild's own body
// (internal/brain/buildcert.go), duplicated here (rather than called) since
// this leaf package cannot import internal/brain (brain already imports
// advpool; the reverse would be a cycle). The verdict is marshaled and
// sha256-digested (mirroring controlgate.PostControlGate's digest pattern)
// so the signed record's output_digest is a tamper-evident fingerprint of
// every Verdict field (subject = repo@commit, byproducts = the digest), then
// certified with a distinct actor so a signed advpool record is never
// confused with a human `corral certify` submission, a merge-gate run, or a
// control-gate run.
//
// CertSigner implements the driver's Signer interface, and is deliberately
// narrower than brain.Options: it takes ONLY the three fields SignVerdict
// actually reads (the signing key, the build store, the transparency
// witness) — no Telemetry field, so unlike the brain-hosted advpoolSigner
// this does not emit the brain's "build_certified" telemetry event; that is
// the one intentional behavior narrowing of this move (the CLI has no
// telemetry store to feed).
type CertSigner struct {
	Key     ed25519.PrivateKey
	Store   *buildstore.Store
	Witness transparency.Witness
}

func (s CertSigner) SignVerdict(ctx context.Context, v Verdict) (int64, string, error) {
	exitCode := 0
	if v.Status != StatusCertified {
		exitCode = 1
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: marshal verdict: %w", err)
	}
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// producedBy surfaces the run's role assignment as human-readable
	// "role:model" strings directly on the signed record (M-2), rather than
	// leaving the models only re-derivable by unpacking output_digest against
	// a separately-stored Verdict. Sorted so the record is deterministic.
	roles := make([]string, 0, len(v.ModelsByRole))
	for role := range v.ModelsByRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	producedBy := make([]string, 0, len(roles))
	for _, role := range roles {
		entry := role + ":" + v.ModelsByRole[role]
		if role == RoleMutantGeneratorShadow {
			// The challenger seat is measurement, not the exam: its mutants
			// never entered the set the dev suite was graded against. An
			// unmarked entry here would read to a record's audience as a model
			// that helped SET the certification, so say plainly that it did
			// not.
			entry += " (non-gating)"
		}
		producedBy = append(producedBy, entry)
	}

	const actor = "corral-advpool"
	steps := []certify.Step{
		{
			Kind:    "context",
			Actor:   actor,
			Subject: v.Repo + "@" + v.Commit,
			Detail: map[string]any{
				"repo":   v.Repo,
				"commit": v.Commit,
				"branch": "",
			},
		},
		{
			Kind:    "execution",
			Actor:   actor,
			Subject: "corral/adversarial-pool",
			Detail: map[string]any{
				"exit_code":       exitCode,
				"ok":              exitCode == 0,
				"duration_s":      0.0,
				"output_digest":   digest,
				"regions_total":   v.RegionsTotal,
				"regions_probed":  v.RegionsProbed,
				"dropped_regions": v.DroppedRegions,
			},
		},
	}
	built, head := certify.BuildLedger(steps)

	br := certify.BuildRecord{
		Repo:         v.Repo,
		Commit:       v.Commit,
		Actor:        actor,
		Command:      "corral/adversarial-pool",
		ExitCode:     exitCode,
		OutputDigest: digest,
		ProducedBy:   producedBy,
	}
	stmt := certify.BuildAttestation(br, head)

	// Sign the FULL canonical statement (not just the head) as a DSSE
	// envelope: a head-only signature leaves the predicate freely editable in
	// storage without invalidating the signature. The envelope embeds its own
	// copy of the canonical statement bytes it signed, so a later VerifyDSSE
	// call checks the identical bytes the signature covers with no separate
	// canonical-bytes column to keep in sync.
	envelope, err := certify.SignDSSE(stmt, s.Key, "brain")
	if err != nil {
		return 0, "", fmt.Errorf("advpool: signing statement: %w", err)
	}
	canonical, err := certify.CanonicalStatement(stmt)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: canonicalizing statement: %w", err)
	}

	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: marshaling steps: %w", err)
	}

	// Anchor the signed envelope to the transparency witness — an ADDITIONAL
	// trustless guarantee, never a build-blocking gate: an unreachable log
	// must degrade the record to anchored=false, not fail the run. s.Witness
	// == nil means anchoring is disabled entirely (same outcome).
	var rekorJSON string
	var anchored bool
	if s.Witness != nil {
		entry, anchorErr := s.Witness.Anchor(ctx, envelope)
		if anchorErr != nil {
			// Loud but keyless: never log the envelope's signing key material
			// (there is none here — envelope/entry are both public artifacts —
			// but keep the message scoped to repo/commit for consistency with
			// the brain's own warning style).
			log.Printf("advpool: transparency witness unreachable for %s@%s, degrading to anchored=false: %v", v.Repo, v.Commit, anchorErr)
		} else {
			entryJSON, marshalErr := json.Marshal(entry)
			if marshalErr != nil {
				log.Printf("advpool: encoding transparency entry for %s@%s, degrading to anchored=false: %v", v.Repo, v.Commit, marshalErr)
			} else {
				rekorJSON = string(entryJSON)
				anchored = true
			}
		}
	}

	// pass mirrors the "execution" step's own ok field above — it's the same
	// exit_code == 0 check, denormalized to a queryable column so a dashboard
	// doesn't have to unpack steps/statement JSON per row for a cheap status
	// filter.
	pass := exitCode == 0

	id, err := s.Store.Save(v.Repo, v.Commit, "", actor, head, string(envelope), string(canonical), string(stepsJSON), rekorJSON, anchored,
		"", "", "", "", pass)
	if err != nil {
		return 0, "", err
	}
	return id, head, nil
}
