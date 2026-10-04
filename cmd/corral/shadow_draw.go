// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

// shadowMemberRunnable is the per-member check: a cloud model's credential,
// nothing for a local one — the rule any seat that may hold a local model
// gets. A variable so a test can pin it: the real credential store is opened
// once per process, so no environment variable set inside a test can
// reliably make a key absent.
var shadowMemberRunnable = func(model string) error {
	_, err := agentbackend.ForModelOrLocal(model)
	return err
}

// shadowPoolSeat is one drawable seat: its role, its two flags, and the
// verdict seat it challenges (for the "compares a model against itself"
// warning and for which history roles to read).
type shadowPoolSeat struct {
	role, base, modelFlag, poolFlag string
	model, pool                     *string
}

func shadowPoolSeats(f *shadowSeatFlags) []shadowPoolSeat {
	return []shadowPoolSeat{
		{advpool.RoleMutantGeneratorShadow, advpool.RoleMutantGenerator, "shadow-model", "shadow-pool", f.model, f.pool},
		{advpool.RoleTestWriterShadow, advpool.RoleTestWriter, "shadow-writer-model", "shadow-writer-pool", f.writerModel, f.writerPool},
	}
}

// validateShadowPools checks every member of every pool, before anything is
// drawn: a member that only failed when drawn would make a command pass ten
// times and fail on the eleventh. The seed is parsed here too, so doctor
// refuses exactly what certify would.
//
// checkCredentials is false only for certify --repo --dry-run, the free
// inventory, which demands no key for any seat: a malformed pool is still
// refused there, but a member's missing credential is a run's problem.
func validateShadowPools(cmdName, repoRoot string, f *shadowSeatFlags, primary map[string]string, checkCredentials bool, stderr io.Writer) error {
	cands, err := shadowCandidates(cmdName, repoRoot, f, primary, checkCredentials, stderr)
	if err != nil {
		return err
	}
	// The seed too: a door that accepted --shadow-seed and never read it
	// would be a flag silently ignored.
	_, _, err = shadowSeed(*f.seed, len(cands) > 0)
	return err
}

// shadowSeed parses --shadow-seed. given is false when none was passed (the
// caller draws a fresh one); a seed with no pool to replay is refused.
func shadowSeed(text string, pooled bool) (seed uint64, given bool, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false, nil
	}
	if !pooled {
		return 0, false, fmt.Errorf("--shadow-seed is set but no --shadow-pool or --shadow-writer-pool is: there is no draw to replay")
	}
	if seed, err = shadowpool.ParseSeed(text); err != nil {
		return 0, false, err
	}
	return seed, true, nil
}

func shadowCandidates(cmdName, repoRoot string, f *shadowSeatFlags, primary map[string]string, checkCredentials bool, stderr io.Writer) (map[string][]shadowpool.Candidate, error) {
	out := map[string][]shadowpool.Candidate{}
	for _, s := range shadowPoolSeats(f) {
		pool := strings.TrimSpace(*s.pool)
		if pool == "" {
			continue
		}
		if strings.TrimSpace(*s.model) != "" {
			return nil, fmt.Errorf("--%s and --%s are both set: a challenger seat is either named or drawn, not both", s.modelFlag, s.poolFlag)
		}
		members, err := shadowpool.ParsePool(s.poolFlag, pool)
		if err != nil {
			return nil, err
		}
		// The verdict seat's model, resolved the way its own flag will be:
		// it reaches here as typed, and an alias never equals a concrete
		// member. A resolution error is not this check's to report — the
		// seat's own resolution refuses it moments later.
		primaryModel := primary[s.base]
		if primaryModel != "" {
			_, _ = resolveSeatRegistry(cmdName, repoRoot, []seatFlag{{flag: s.base, role: s.base, val: &primaryModel}}, io.Discard)
		}
		for _, typed := range members {
			concrete := typed
			if _, err := resolveSeatRegistry(cmdName, repoRoot, []seatFlag{{flag: s.poolFlag, role: s.role, val: &concrete}}, io.Discard); err != nil {
				return nil, fmt.Errorf("--%s member %q: %v", s.poolFlag, typed, err)
			}
			if checkCredentials {
				if err := shadowMemberRunnable(concrete); err != nil {
					return nil, fmt.Errorf("--%s member %q cannot run: %v — every member is checked before the draw, so fix or remove it", s.poolFlag, typed, err)
				}
			}
			if primaryModel != "" && strings.TrimSpace(primaryModel) == concrete {
				fmt.Fprintf(stderr, "%s: warning: --%s member %q is the %s's own model — if it is drawn, the head-to-head compares a model against itself\n", cmdName, s.poolFlag, typed, s.base)
			}
			out[s.role] = append(out[s.role], shadowpool.Candidate{Typed: typed, Concrete: concrete})
		}
	}
	return out, nil
}

func drawShadowSeats(cmdName, repoRoot, lang string, f *shadowSeatFlags, primary map[string]string, storePath string, stderr io.Writer) ([]shadowpool.Selection, error) {
	cands, err := shadowCandidates(cmdName, repoRoot, f, primary, true, stderr)
	if err != nil {
		return nil, err
	}
	seed, given, err := shadowSeed(*f.seed, len(cands) > 0)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}
	if !given {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fmt.Errorf("drawing a seed: %v", err)
		}
		seed = binary.LittleEndian.Uint64(b[:])
	}
	// History is best-effort, like every scorecard read: a store that will
	// not open leaves every member on the flat prior, and the record says
	// "not read" — never a measured zero.
	store, openErr := bugcatch.Open(storePath)
	if store != nil {
		defer store.Close()
	}
	storeLang := lang
	if lang == shadowpool.LangAny {
		storeLang = ""
	}
	var sels []shadowpool.Selection
	for _, s := range shadowPoolSeats(f) {
		cs := cands[s.role]
		if len(cs) == 0 {
			continue
		}
		hist, rows, read := map[string]shadowpool.Counts{}, 0, false
		if openErr == nil {
			ev, _, err := store.Evidence(context.Background(), []string{s.base, s.role}, storeLang)
			if err == nil {
				// Rows about THIS pool's members only: the roles also hold
				// the primary's record (and any other model's), which is not
				// evidence behind this draw and must not be signed as such.
				read = true
				members := map[string]bool{}
				for _, c := range cs {
					if !members[c.Concrete] {
						members[c.Concrete] = true
						rows += ev[c.Concrete].Rows
					}
				}
				for m, e := range ev {
					if s.base == advpool.RoleTestWriter {
						hist[m] = shadowpool.Counts{Successes: e.Catches, Trials: e.Opportunities}
					} else {
						hist[m] = shadowpool.Counts{Successes: e.Survived, Trials: e.Planted}
					}
				}
			}
		}
		sel := shadowpool.Draw(s.role, lang, cs, hist, read, rows, seed)
		*s.model = sel.Chosen
		fmt.Fprintf(stderr, "%s: %s challenger drawn from a pool of %d: %s — seed %s, history %s\n",
			cmdName, s.base, len(cs), describeDraw(sel), sel.Seed, describeHistory(sel))
		sels = append(sels, sel)
	}
	return sels, nil
}

func describeDraw(sel shadowpool.Selection) string {
	for _, m := range sel.Members {
		if m.Model == sel.Chosen {
			return fmt.Sprintf("%s (α=%g β=%g, sampled %.2f)", m.Model, m.Alpha, m.Beta, m.Sample)
		}
	}
	return sel.Chosen
}

func describeHistory(sel shadowpool.Selection) string {
	if sel.History == shadowpool.HistoryNotRead {
		return "NOT READ (scorecard unavailable — every member on the flat prior)"
	}
	return fmt.Sprintf("%d row(s) for %s", sel.HistoryRows, sel.Lang)
}
