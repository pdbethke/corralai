// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
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

	for _, c := range cands {
		goal, ok, err := gs.GoalFor(c)
		if err != nil {
			// A per-candidate goal failure is NOT fatal to the scan and is NOT
			// ungoaled: the source of the goal failed, which says nothing about
			// the file. Account it under its own reason and keep going, so one
			// rate-limited file cannot cost the operator the other 24.
			excl = append(excl, Exclusion{Path: c.Path, Reason: ReasonDeriveFailed})
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
		testDigest, err := DigestFile(root, c.TestPath)
		if err != nil {
			return nil, nil, err
		}

		key := KeyInputs{
			SourceDigest:      srcDigest,
			PackageDigest:     pkgDigest,
			GoalDigest:        digestString(goal.Text),
			TestSurfaceDigest: testDigest,
			EngineVersion:     cfg.EngineVersion,
			ModelSet:          cfg.ModelSet,
			AuditConfig:       cfg.AuditConfig,
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
