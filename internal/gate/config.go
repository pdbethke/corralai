// SPDX-License-Identifier: Elastic-2.0

package gate

import "strconv"
import "strings"

// ParsePolicies parses CORRALAI_GATE_POLICIES: semicolon-separated policy
// entries, each a comma-separated list of key=value pairs —
// "repo=owner/name,base=main,net=false,timeout=600,cmd=go test ./...". An
// empty raw string yields (nil, nil): the merge-gate feature's off switch. A
// malformed entry (missing the required repo= or cmd=) is skipped and
// reported in bad rather than aborting the whole parse — one bad entry in
// an operator's env var must not silently disable every other repo's gate
// (degrade-never-block, same directive as the poller).
//
// cmd= MUST be the LAST field in an entry. Everything from "cmd=" to the end
// of the entry is the command VERBATIM — commas included, never
// comma-split — so a check like "go test -run A,B ./..." isn't silently
// truncated to "go test -run A" (a truncated cmd is a WEAKER command that
// could exit 0 and post a wrongful "success"; this is the one
// operator-reachable path that could manufacture a green gate, so cmd
// parsing fails loudly rather than guessing). An entry with no cmd= at all
// is malformed (reported in bad), never silently accepted with an
// empty/default command.
//
// base= may repeat within an entry (space or "|"-joined isn't supported —
// only the last base= wins per entry today; multi-base policies are
// expressed as multiple semicolon-separated entries sharing a repo). An
// omitted base= means "all bases" (Policy.Base == nil). An omitted
// context defaults to "corral/gate". An omitted net= defaults to false
// (no network — fail-closed default, matching the runner's own posture).
// An omitted (or non-numeric) timeout= leaves Policy.TimeoutS at 0, which
// the runner turns into DefaultGateTimeout.
// maxGateTimeoutS bounds timeout= well below the point where
// time.Duration(n)*time.Second overflows int64 (~292 years), while leaving
// room for any real check: 24 hours.
const maxGateTimeoutS = 24 * 60 * 60

// policyFields are the keys ParsePolicies understands. strayFieldAfterCmd
// derives its check from this list rather than repeating it, so a key added
// here is covered without a second edit.
var policyFields = []string{"repo", "base", "context", "net", "timeout"}

// strayFieldAfterCmd returns the name of a known policy field that appears
// after cmd= (where it would be swallowed into the command verbatim), or "".
func strayFieldAfterCmd(cmdVal string) string {
	for _, f := range policyFields {
		if strings.Contains(cmdVal, ","+f+"=") {
			return f
		}
	}
	return ""
}

func ParsePolicies(raw string) (policies []Policy, bad []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// THE SEMICOLON IS THE SAME DEFECT THE COMMA NOTE ABOVE GUARDS AGAINST.
	// cmd= runs to the end of its ENTRY, and entries are ';'-separated, so a
	// command containing ';' — "cmd=go vet ./... ; go test ./..." under sh -c
	// — was split here BEFORE cmd= was isolated: the first half was accepted
	// as a complete policy carrying the WEAKER command, and only the orphaned
	// tail was reported. That is precisely the "truncated cmd manufactures a
	// green gate" path this file claims to prevent, arriving through the other
	// delimiter. (Cold review 2026-09-12, R2 — reproduced, high.)
	//
	// It is refused rather than repaired. Re-joining the fragments would be
	// guessing: an operator who simply forgot repo= on a second entry would
	// have it silently welded onto the previous command. cmd parsing fails
	// loudly, so both fragments are reported and NEITHER policy is accepted.
	frags := strings.Split(raw, ";")
	for i, entry := range frags {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// A fragment with no key=value at all cannot be a policy entry; if the
		// one before it declared a cmd=, this is that command's missing tail.
		if i > 0 && !strings.Contains(entry, "=") && strings.Contains(frags[i-1], "cmd=") {
			bad = append(bad, entry+" (a ';' inside cmd= truncated the previous entry's command — the entry before this one was NOT applied; remove the ';' or express the steps as one command)")
			// Drop the truncated policy we just accepted, if we accepted it.
			if n := len(policies); n > 0 && strings.Contains(frags[i-1], policies[n-1].Repo) {
				bad = append(bad, strings.TrimSpace(frags[i-1])+" (command truncated at ';')")
				policies = policies[:n-1]
			}
			continue
		}

		// cmd= must be the last field: split the entry at "cmd=" so the
		// tail (the command) is captured verbatim, commas and all, instead
		// of being torn apart by the generic comma-split below.
		var head, cmdVal string
		cmdSeen := false
		switch {
		case strings.HasPrefix(entry, "cmd="):
			cmdVal = entry[len("cmd="):]
			cmdSeen = true
		case strings.Contains(entry, ",cmd="):
			idx := strings.Index(entry, ",cmd=")
			head = entry[:idx]
			cmdVal = entry[idx+len(",cmd="):]
			cmdSeen = true
		}
		cmdVal = strings.TrimSpace(cmdVal)

		pol := Policy{Context: "corral/gate"}
		var repoSeen bool
		var badField string
		if cmdSeen {
			// A POLICY FIELD PLACED AFTER cmd= was swallowed into the command
			// rather than reported: "repo=o/r,cmd=make test,base=release" was
			// accepted with CheckCmd ["make","test,base=release"] AND Base nil,
			// which gates every base instead of one — a wider policy than the
			// operator wrote, silently, with nothing in bad. The doc says cmd=
			// must be LAST and that parsing fails loudly; now it does.
			// (Cold review 2026-09-12, R8.)
			if stray := strayFieldAfterCmd(cmdVal); stray != "" {
				bad = append(bad, entry+" ("+stray+"= appears after cmd=, which takes the rest of the entry verbatim; move it before cmd=)")
				continue
			}
			if fields := strings.Fields(cmdVal); len(fields) > 0 {
				pol.CheckCmd = fields
			} else {
				cmdSeen = false // "cmd=" with an empty/whitespace-only tail is not a real command
			}
		}

		for _, kv := range strings.Split(head, ",") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			key, val, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			switch key {
			case "repo":
				pol.Repo = val
				repoSeen = val != ""
			case "base":
				if val != "" {
					pol.Base = []string{val}
				}
			case "context":
				if val != "" {
					pol.Context = val
				}
			case "net":
				pol.AllowNet = val == "true" || val == "1"
			case "timeout":
				// A huge timeout= parsed fine and then OVERFLOWED in
				// time.Duration(n)*time.Second to a negative duration, which
				// the sandbox turns into its own 60s default — the exact
				// outcome DefaultGateTimeout's comment says blocks merges,
				// reached silently. Bound it and say so.
				// (Cold review 2026-09-12, R7.)
				if n, err := strconv.Atoi(val); err == nil {
					switch {
					case n < 0:
						badField = "timeout=" + val + " is negative"
					case n > maxGateTimeoutS:
						badField = "timeout=" + val + "s exceeds the maximum " + strconv.Itoa(maxGateTimeoutS) + "s"
					default:
						pol.TimeoutS = n
					}
				} else if val != "" {
					badField = "timeout=" + val + " is not a number"
				}
			}
		}

		if badField != "" {
			bad = append(bad, entry+" ("+badField+")")
			continue
		}
		if !repoSeen || !cmdSeen {
			bad = append(bad, entry)
			continue
		}
		policies = append(policies, pol)
	}
	return policies, bad
}
