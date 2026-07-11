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
func ParsePolicies(raw string) (policies []Policy, bad []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
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
		if cmdSeen {
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
				if n, err := strconv.Atoi(val); err == nil {
					pol.TimeoutS = n
				}
			}
		}

		if !repoSeen || !cmdSeen {
			bad = append(bad, entry)
			continue
		}
		policies = append(policies, pol)
	}
	return policies, bad
}
