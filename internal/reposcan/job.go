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
	Goal                 Goal
	CacheKey             string
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
	// FileScopedTests is true when this scan grades each file against its OWN
	// paired test file only (--scope-tests on a language with a verified
	// per-file invocation, or an explicit `-- <cmd>` that names one test
	// file). It decides what TestSurfaceDigest has to cover: one file, or the
	// whole suite. See DigestTestSurface.
	FileScopedTests bool
	// TestSurfacePaths are test files that grade in a whole-suite run but are
	// nobody's paired test — shared helpers, conftest.py, fixtures. Enumerate
	// hands them back as `is-test` exclusions, so they never appear as a
	// Candidate.TestPath, yet weakening one really does change what the suite
	// grades. Ignored when FileScopedTests is set. No new walk: the caller
	// already has this list.
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

		key := KeyInputs{
			SourceDigest:      srcDigest,
			PackageDigest:     pkgDigest,
			GoalDigest:        digestString(goal.Text),
			TestSurfaceDigest: testDigest,
			EngineVersion:     cfg.EngineVersion,
			ModelSet:          cfg.ModelSet,
			AuditConfig:       cfg.AuditConfig,
			Substrate:         cfg.Substrate,
		}.CacheKey()

		jobs = append(jobs, Job{
			Owner: cfg.Owner, Repo: cfg.Repo, Commit: cfg.Commit,
			Path: c.Path, TestPath: c.TestPath, Lang: c.Lang,
			Goal: goal, CacheKey: key,
		})
	}
	return jobs, excl, nil
}

func digestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
