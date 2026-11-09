// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProofFloor is the oldest grading generation whose entries count as proof
// that corral grades a language. It starts at the generation that carries
// kill confirmation; raising it retires proofs graded under older rules.
// It must never exceed reposcan.VerdictGeneration (a test in reposcan holds
// that), or no run corral could produce would qualify.
const ProofFloor = "40"

// ProofRun is one qualifying entry, as the view shows it.
type ProofRun struct {
	Hash, Repo, Commit, Path          string
	Runner, CorralVersion, Generation string
	RunURL                            string // non-empty = ran on a CI runner
	KillRate                          *float64
	ProvenMissed                      int
	StartedAt                         time.Time
}

// LanguageProof is what the record can show for one language.
type LanguageProof struct {
	Local, CI  *ProofRun // newest qualifying of each kind; nil = none
	ProvedAGap bool      // any qualifying entry for this language has ProvenMissed > 0
	BelowFloor int       // entries that would qualify but for the floor
	Unchecked  int       // entries that would qualify but whose signature was not checked
}

// NormalizeRepo reduces the spellings of a repository (URL, ssh, .git
// suffix, case) to "host/owner/name" in lower case.
func NormalizeRepo(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		s = strings.TrimPrefix(s, p)
	}
	// user@ prefix (git@github.com:..., https://token@host/...).
	head := strings.IndexAny(s, "/:")
	if head < 0 {
		head = len(s)
	}
	if at := strings.Index(s[:head], "@"); at >= 0 {
		s = s[at+1:]
	}
	// scp-like host:owner/name
	if c := strings.Index(s, ":"); c >= 0 && (!strings.Contains(s, "/") || c < strings.Index(s, "/")) {
		s = s[:c] + "/" + s[c+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	return strings.ToLower(strings.Trim(s, "/"))
}

// LanguageProofs decides, in one place, which signed entries prove corral
// grades a language. runnerLangs maps a runner ID (Scan.Runner) to the
// languages it runs; the caller builds it from lang.Runners(), because this
// package cannot import lang (it builds for the browser).
//
// self lists the repositories whose runs are self-audit; there is no
// default, because an empty list would silently count corral grading corral
// as evidence. A language with no qualifying entry and no refusal counted is
// absent from the map.
func LanguageProofs(entries []Entry, checks []ChainCheck, self []string, floor string,
	runnerLangs map[string][]string) (map[string]LanguageProof, error) {
	selfSet := map[string]bool{}
	for _, s := range self {
		if n := NormalizeRepo(s); n != "" {
			selfSet[n] = true
		}
	}
	if len(selfSet) == 0 {
		return nil, errors.New("ledger: LanguageProofs needs self: the repositories whose runs are self-audit")
	}
	floorN, err := strconv.ParseUint(floor, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ledger: LanguageProofs floor %q is not a decimal generation", floor)
	}
	byHash := map[string]ChainCheck{}
	for _, c := range checks {
		byHash[c.Hash] = c
	}

	out := map[string]LanguageProof{}
	newest := map[string]time.Time{} // key: lang + "\x00" + "local"/"ci"
	for _, e := range ScanEntries(entries) {
		sc := e.Bundle.Scan
		// Conditions that disqualify without being counted.
		if sc.Repo == "" || sc.RepoIdentity != "git" || sc.Runner == "" {
			continue
		}
		if sc.TreeDirty != nil && *sc.TreeDirty {
			continue
		}
		if selfSet[NormalizeRepo(sc.Repo)] {
			continue
		}
		chk, ok := byHash[e.Hash]
		if !ok || !chk.HashOK || !chk.LinkOK || chk.Problem != "" {
			continue
		}
		// Signature: unchecked is counted; checked-and-bad is not.
		sigOK := chk.SigChecked && chk.SigOK && chk.Signer != ""
		unchecked := !chk.SigChecked
		if chk.SigChecked && !sigOK {
			continue
		}
		gen, gerr := strconv.ParseUint(sc.VerdictGeneration, 10, 64)
		if gerr != nil {
			continue
		}
		belowFloor := gen < floorN

		var started time.Time
		if sc.StartedAt != nil {
			started = *sc.StartedAt
		}
		seen := map[string]bool{}
		for _, f := range e.Bundle.Files {
			if f.Lang == "" || f.Audited <= 0 || f.BaselineFailed || f.MutantsGraded <= 0 ||
				f.CacheHit || f.ReusedFromScanID != nil {
				continue
			}
			lp := out[f.Lang]
			if !seen[f.Lang] {
				seen[f.Lang] = true
				// A near-miss is counted only when it fails that ONE
				// condition; failing both is counted under neither.
				if belowFloor || unchecked {
					if belowFloor && !unchecked {
						lp.BelowFloor++
						out[f.Lang] = lp
					} else if unchecked && !belowFloor {
						lp.Unchecked++
						out[f.Lang] = lp
					}
					continue
				}
			} else if belowFloor || unchecked {
				continue
			}
			run := &ProofRun{
				Hash: e.Hash, Repo: sc.Repo, Commit: sc.Commit, Path: f.Path,
				CorralVersion: sc.CorralVersion, Generation: sc.VerdictGeneration,
				RunURL: sc.RunURL, KillRate: f.KillRate, ProvenMissed: f.ProvenMissed,
				StartedAt: started,
			}
			for _, l := range runnerLangs[sc.Runner] {
				if l == f.Lang {
					run.Runner = sc.Runner
					break
				}
			}
			if f.ProvenMissed > 0 {
				lp.ProvedAGap = true
			}
			slot, kind := &lp.Local, "local"
			if sc.RunURL != "" {
				slot, kind = &lp.CI, "ci"
			}
			key := f.Lang + "\x00" + kind
			// ScanEntries is in chain order, so >= lets a later entry win a tie.
			if *slot == nil || !started.Before(newest[key]) {
				*slot = run
				newest[key] = started
			}
			out[f.Lang] = lp
		}
	}
	return out, nil
}
