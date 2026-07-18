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
		return p.TestPath(codePath)
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
}

func (s JailScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd)
	if err != nil {
		return 0, nil, fmt.Errorf("advpool: score: %w", err)
	}
	byID := make(map[string]adequacy.Mutant, len(mutants))
	for _, m := range mutants {
		byID[m.ID] = m
	}
	survivors := make([]adequacy.Mutant, 0, len(rep.Survived))
	for _, id := range rep.Survived {
		if m, ok := byID[id]; ok {
			survivors = append(survivors, m)
		}
	}
	return rep.KillRate(), survivors, nil
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

	compiles, err := v.Jail.RunTest(ctx, ws, p.CompileCheck(codePath, testPath))
	if err != nil {
		return fmt.Errorf("advpool: compile-verify test: %w", err)
	}
	if !compiles {
		return fmt.Errorf("advpool: test does not compile")
	}
	return nil
}

func (v JailValidator) ParseMutants(raw string) ([]adequacy.Mutant, error) {
	return testgen.ParseMutantsOutput(raw)
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
		producedBy = append(producedBy, role+":"+v.ModelsByRole[role])
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
				"exit_code":     exitCode,
				"ok":            exitCode == 0,
				"duration_s":    0.0,
				"output_digest": digest,
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
