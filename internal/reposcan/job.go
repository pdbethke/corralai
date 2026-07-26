// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
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
func EmitJobs(cfg EmitConfig, cands []Candidate, gs GoalSource) ([]Job, []Exclusion, error) {
	var jobs []Job
	var excl []Exclusion

	for _, c := range cands {
		goal, ok, err := gs.GoalFor(c)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			excl = append(excl, Exclusion{Path: c.Path, Reason: ReasonUngoaled})
			continue
		}

		srcDigest, err := DigestFile(filepath.Join(cfg.Root, c.Path))
		if err != nil {
			return nil, nil, err
		}
		pkgDigest, err := DigestDir(filepath.Join(cfg.Root, filepath.Dir(c.Path)))
		if err != nil {
			return nil, nil, err
		}
		testDigest, err := DigestFile(filepath.Join(cfg.Root, c.TestPath))
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
