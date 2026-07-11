// SPDX-License-Identifier: Elastic-2.0

package gate

import "strings"

// ParsePolicies parses CORRALAI_GATE_POLICIES: semicolon-separated policy
// entries, each a comma-separated list of key=value pairs —
// "repo=owner/name,base=main,cmd=go test ./...,net=false". An empty raw
// string yields (nil, nil): the merge-gate feature's off switch. A
// malformed entry (missing the required repo= or cmd=) is skipped and
// reported in bad rather than aborting the whole parse — one bad entry in
// an operator's env var must not silently disable every other repo's gate
// (degrade-never-block, same directive as the poller).
//
// base= may repeat within an entry (space or "|"-joined isn't supported —
// only the last base= wins per entry today; multi-base policies are
// expressed as multiple semicolon-separated entries sharing a repo). An
// omitted base= means "all bases" (Policy.Base == nil). An omitted
// context defaults to "corral/gate". An omitted net= defaults to false
// (no network — fail-closed default, matching the runner's own posture).
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
		pol := Policy{Context: "corral/gate"}
		var repoSeen, cmdSeen bool
		for _, kv := range strings.Split(entry, ",") {
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
			case "cmd":
				fields := strings.Fields(val)
				if len(fields) > 0 {
					pol.CheckCmd = fields
					cmdSeen = true
				}
			case "context":
				if val != "" {
					pol.Context = val
				}
			case "net":
				pol.AllowNet = val == "true" || val == "1"
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
