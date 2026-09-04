// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
)

// ReasonUngoaled marks a candidate the GoalSource declined to supply a goal
// for. Fail-closed: excluded from the scored surface, never given a made-up
// goal.
const ReasonUngoaled = "ungoaled"

// Job is the unit of audit work. It is owner-keyed from day one: `Owner` is
// the forge-side account in the local scan and becomes the tenant identifier
// in the hosted service, so tenancy is never retrofitted into the envelope.
type Job struct {
	Owner, Repo, Commit  string
	Path, TestPath, Lang string
	// CoveringTestPath is Candidate.CoveringTestPath, carried through for an
	// evidence-only candidate (TestPath == ""): the authored-landing hint —
	// "the authored test lands beside CoveringTestPath" — since there is no
	// TestPath directory to land it beside. "" for a pairing-based
	// candidate, which already has one.
	CoveringTestPath string
	// CoveringTests is Candidate.CoveringTests, carried through for the
	// ledger: how many tests the evidence showed execute this file. nil
	// when the evidence never measured it (no evidence run, or a
	// pairing-only fallback — see WidenCandidacyByEvidence).
	CoveringTests *int
	Goal          Goal
	CacheKey      string
	// GoalReused mirrors GoalWasReused(Goal): this file's goal was served
	// by a CachingGoalSource from a prior scan over identical bytes, rather
	// than freshly derived. Carried on the job so every downstream
	// disclosure hop (WeakFile, the ledger, the attestation, the warehouse)
	// can read it without re-deriving the same fact from Goal.Provenance.
	GoalReused bool
}

// EmitConfig is the scan-wide context every job inherits.
type EmitConfig struct {
	Owner, Repo, Commit, Root            string
	EngineVersion, ModelSet, AuditConfig string
	// Substrate is where this scan's audits run — SubstrateJail or
	// SubstrateWorkspace. Carried into every job's KeyInputs so a verdict
	// earned under bwrap and one earned in a CI runner's own checkout never
	// key identically: without it, a cached jail verdict would satisfy a
	// seal claiming runner provenance.
	Substrate string
	// FileAuditConfig, when set, contributes a per-candidate component to
	// that job's cache key, appended to AuditConfig. It exists because the
	// grading MODE can differ per file inside one scan (selection for most
	// files, the whole suite for one the evidence never saw), and a verdict
	// earned under one mode must never be served for the other. The
	// scan-level AuditConfig can only say that selection RAN.
	//
	// Return "" to contribute nothing — a job then keys exactly as it would
	// with no hook at all, which is what a dry run (which grades nothing)
	// and every pre-selection caller rely on.
	FileAuditConfig func(c Candidate) string
	// FileScopedTests is true when this scan grades each file against its OWN
	// paired test file only (--scope-tests on a language with a verified
	// per-file invocation, or an explicit `-- <cmd>` that names one test
	// file). It decides what TestSurfaceDigest has to cover: one file, or the
	// whole suite. See DigestTestSurface.
	FileScopedTests bool
	// TestSurfacePaths are files that grade in a whole-suite run but are
	// nobody's paired test, so they never appear as a Candidate.TestPath —
	// yet changing one really does change what the suite measures.
	//
	// Two kinds, and only the first is caught by a filename marker:
	//   - files Enumerate rejected as `is-test` (foo_test.go, spec_helper.rb)
	//     or `test-support` (anything under a language's test root that is
	//     not itself a test — see ReasonTestSupport).
	//   - everything else living in a directory that holds a test file:
	//     conftest.py, helpers.py, fixtures.py, jest.setup.js, golden JSON.
	//     These match no test-filename marker, so Enumerate classifies them
	//     `no-paired-test` or `no-language`; the CALLER, not this package,
	//     decides they belong to the surface (see testSurfacePaths in
	//     cmd/corral). Deliberately over-inclusive.
	//   - regular files under a `testdata` segment. That directory is in
	//     skipDirs, so a Go golden comes back `skipped-dir` and its directory
	//     holds no recognized test — neither rule above reaches it. Weakening
	//     a golden is the commonest way a Go suite changes what it measures.
	//
	// WHAT IS STILL NOT COVERED, so a maintainer can decide how far to trust
	// the key:
	//   - a fixture file OUTSIDE any test root, in a directory that holds no
	//     test file. A repo-root conftest.py with all its tests under tests/
	//     is the live example: it configures every one of them and reaches
	//     no key. (Under a test root it is `test-support` and digested.)
	//   - a non-regular entry under testdata (it is skipped rather than
	//     erroring, since a scan-fatal error would be the worse failure).
	//   - THE FILE-SCOPED PATH IGNORES THIS LIST ENTIRELY. When
	//     FileScopedTests is set the digest is the one paired test file, so
	//     `-- pytest tests/test_a.py` — which really does load
	//     tests/conftest.py — leaves the key unmoved when that fixture is
	//     weakened. That is an OPEN WRONG-HIT PATH, not a design statement:
	//     the cache can serve a kill rate for a grading surface that changed.
	//
	// No new walk: the caller already has this list.
	TestSurfacePaths []string
}

// EmitJobs turns candidates into job envelopes, computing each one's cache
// key. Candidates without a goal come back as exclusions.
//
// Every digest is read through an *os.Root opened on cfg.Root, so a symlink
// inside the scanned checkout cannot make the scan hash — and therefore the
// scan's own record of — a file outside the repository.
func EmitJobs(cfg EmitConfig, cands []Candidate, gs GoalSource) ([]Job, []Exclusion, error) {
	var jobs []Job
	var excl []Exclusion

	if len(cands) == 0 {
		return nil, nil, nil
	}
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()

	// The grading surface decides the digest. Unless this scan grades each
	// file against its own paired test (see EmitConfig.FileScopedTests), the
	// command every baseline and every mutant runs is the project's WHOLE
	// recursive suite — so TestSurfaceDigest has to cover the whole suite, not
	// the one paired file. Computed ONCE for the scan, not per candidate:
	// every job on this path is graded by the same suite.
	suiteDigest := ""
	if !cfg.FileScopedTests {
		paths := make([]string, 0, len(cands)+len(cfg.TestSurfacePaths))
		for _, c := range cands {
			if c.TestPath != "" {
				paths = append(paths, c.TestPath)
			}
		}
		paths = append(paths, cfg.TestSurfacePaths...)
		d, derr := DigestTestSurface(root, paths)
		if derr != nil {
			// NOTE, because this is a WIDER hard failure than before the whole
			// suite was keyed: any unreadable path in the surface — a test
			// file removed or made non-regular between Enumerate and EmitJobs,
			// including one belonging to a candidate this scan never selected
			// — now aborts the ENTIRE scan, where previously only a SELECTED
			// candidate's own test file could do that. Fail-closed on purpose:
			// a surface we cannot read is a surface we cannot key, and a key
			// computed over a guess would sign an unmeasured claim.
			return nil, nil, derr
		}
		suiteDigest = d
	}

	for _, c := range cands {
		goal, ok, err := gs.GoalFor(c)
		if err != nil {
			// A per-candidate goal failure is NOT fatal to the scan and is NOT
			// ungoaled: the source of the goal failed, which says nothing about
			// the file. Account it under its own reason and keep going, so one
			// rate-limited file cannot cost the operator the other 24.
			reason := ReasonDeriveFailed
			// ...except when the failure is a property of the FILE. An oversized
			// generated blob is not an outage, and filing it under derive-failed
			// would tell an operator to go check their API key.
			if errors.Is(err, ErrSourceTooLarge) {
				reason = ReasonSourceTooLarge
			}
			excl = append(excl, Exclusion{Path: c.Path, Reason: reason})
			continue
		}
		if !ok {
			excl = append(excl, Exclusion{Path: c.Path, Reason: ReasonUngoaled})
			continue
		}

		srcDigest, err := DigestFile(root, c.Path)
		if err != nil {
			return nil, nil, err
		}
		pkgDigest, err := DigestDir(root, path.Dir(c.Path))
		if err != nil {
			return nil, nil, err
		}
		testDigest := suiteDigest
		if cfg.FileScopedTests {
			// One file is genuinely the whole grading surface here, so keying
			// on the whole suite would throw away every verdict in the repo
			// for a change that cannot reach them.
			testDigest, err = DigestFile(root, c.TestPath)
			if err != nil {
				return nil, nil, err
			}
		}

		// The per-file grading mode, folded into the same component the
		// scan-wide settings use. Joined with CanonicalKV's own delimiter
		// (a comma) so the whole string stays one well-formed name=value
		// list rather than two serializations that could drift apart.
		auditConfig := cfg.AuditConfig
		if cfg.FileAuditConfig != nil {
			if extra := cfg.FileAuditConfig(c); extra != "" {
				if auditConfig == "" {
					auditConfig = extra
				} else {
					auditConfig = auditConfig + "," + extra
				}
			}
		}

		key := KeyInputs{
			SourceDigest:      srcDigest,
			PackageDigest:     pkgDigest,
			GoalDigest:        digestString(goal.Text),
			TestSurfaceDigest: testDigest,
			EngineVersion:     cfg.EngineVersion,
			ModelSet:          cfg.ModelSet,
			AuditConfig:       auditConfig,
			Substrate:         cfg.Substrate,
		}.CacheKey()

		jobs = append(jobs, Job{
			Owner: cfg.Owner, Repo: cfg.Repo, Commit: cfg.Commit,
			Path: c.Path, TestPath: c.TestPath, Lang: c.Lang,
			CoveringTestPath: c.CoveringTestPath,
			CoveringTests:    c.CoveringTests,
			Goal:             goal, CacheKey: key,
			GoalReused: GoalWasReused(goal),
		})
	}
	return jobs, excl, nil
}

func digestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
