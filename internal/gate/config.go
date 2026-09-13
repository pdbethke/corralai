// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PolicyEnvPrefix is where a merge-gate policy lives: ONE policy per
// environment variable, named CORRALAI_GATE_POLICY_<NAME>, mirroring the
// CORRALAI_AGENT_<NAME> convention the review seats already use.
//
// WHY ONE VARIABLE PER POLICY. The previous format packed every policy into a
// single ";"-separated CORRALAI_GATE_POLICIES, and a command is allowed to
// contain a ";" — so the entry separator and the command's own syntax
// collided. Three separate guards were written against that collision and a
// cold reviewer defeated all three:
//
//	round two   split on ';' before isolating cmd=, so "go vet ./... ; go test
//	            ./..." was accepted as the WEAKER "go vet ./..."
//	round three keyed the guard on the absence of '=', defeated by any command
//	            containing a flag like -tags=integration
//	round four  keyed it on the absence of "repo=", defeated by a command
//	            containing "repo=" incidentally: "true; env repo=x false"
//
// Every one of those let a truncated, weaker command post a wrongful success,
// which is the worst thing this package can do. The fourth attempt is not
// another guard. Giving each policy its own variable means the OPERATING
// SYSTEM supplies the separator, and a ";" inside a command can no longer
// collide with anything. The ambiguity is removed rather than policed.
const PolicyEnvPrefix = "CORRALAI_GATE_POLICY_"

// LegacyPolicyEnv is the retired single-variable format. It is not parsed —
// it is REFUSED, loudly, by ParsePolicyEnv. Supporting both would mean two
// parsers for one rule, and a rule living at two doors is the defect this
// package has produced fifteen findings' worth of; see PolicyEnvPrefix.
const LegacyPolicyEnv = "CORRALAI_GATE_POLICIES"

// maxGateTimeoutS bounds timeout= well below the point where
// time.Duration(n)*time.Second overflows int64 (~292 years), while leaving
// room for any real check: 24 hours.
const maxGateTimeoutS = 24 * 60 * 60

// policyFields are the keys a policy understands. strayFieldAfterCmd derives
// its check from this list rather than repeating it, so a key added here is
// covered without a second edit.
var policyFields = []string{"repo", "base", "context", "net", "timeout"}

// strayFieldRE is derived from policyFields and tolerates whitespace on both
// sides of the comma and the '='.
var strayFieldRE = func() map[string]*regexp.Regexp {
	m := make(map[string]*regexp.Regexp, len(policyFields))
	for _, f := range policyFields {
		m[f] = regexp.MustCompile(`,\s*` + regexp.QuoteMeta(f) + `\s*=`)
	}
	return m
}()

// ParsePolicyEnv reads every CORRALAI_GATE_POLICY_<NAME> out of environ (the
// os.Environ() form, "KEY=VALUE") and returns the policies in a DETERMINISTIC
// order — sorted by variable name.
//
// The sort is not cosmetic. Ranging a map and taking what comes is the exact
// non-determinism that produced two findings in this repository within a day
// (an arbitrary Rekor entry chosen on a UUID miss, and the same again in the
// logger). Policies decide which check runs against a pull request; they are
// not allowed to arrive in a different order on different runs.
//
// A malformed policy is reported in bad and SKIPPED, never fatal: one bad
// variable must not silently disable every other repo's gate
// (degrade-never-block, the same directive the poller follows). Because each
// policy now has its own variable, a bad one is isolated by construction —
// under the old format a single stray character could take its neighbours
// with it.
func ParsePolicyEnv(environ []string) (policies []Policy, bad []string) {
	if legacy := envValue(environ, LegacyPolicyEnv); legacy != "" {
		bad = append(bad, LegacyPolicyEnv+" is no longer supported and was IGNORED — its ';' separator collided with commands containing ';', which silently ran a weaker check and posted success. Set one "+PolicyEnvPrefix+"<NAME> per policy instead; each value is the same text between the old ';' separators")
	}

	type named struct{ name, val string }
	var found []named
	for _, kv := range environ {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, PolicyEnvPrefix) {
			continue
		}
		name := strings.TrimPrefix(key, PolicyEnvPrefix)
		if name == "" {
			bad = append(bad, key+" has no name after the prefix")
			continue
		}
		found = append(found, named{name, val})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })

	for _, f := range found {
		pol, reason := ParsePolicy(f.val)
		if reason != "" {
			bad = append(bad, PolicyEnvPrefix+f.name+": "+reason)
			continue
		}
		policies = append(policies, pol)
	}
	return policies, bad
}

// envValue returns the value of key in an os.Environ()-shaped slice, or "".
func envValue(environ []string, key string) string {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// ParsePolicy parses ONE policy value: a comma-separated list of key=value
// pairs — "repo=owner/name,base=main,net=false,timeout=600,cmd=go test ./..."
// — returning a non-empty reason when it is malformed.
//
// cmd= MUST BE LAST, and everything after it is the command VERBATIM to the
// end of the value: commas, semicolons, quotes, newlines and all. There is no
// entry separator left to collide with, so the command needs no escaping and
// corral needs no heuristic to find its end.
//
// A field written after cmd= would be swallowed into the command — which once
// produced a policy gating EVERY base branch when the operator had named one —
// so it is reported rather than absorbed. An empty or whitespace-only command
// is malformed: it would reach the jail as `sh -c ""`, exit 0, and post a
// success for a check that ran nothing.
//
// base= may repeat; only the last wins. An omitted base= means "all bases"
// (Policy.Base == nil). An omitted context= is defaulted at the door that ACTS
// on the policy (Policy.normalized), never here, so the forge and the store
// can never disagree about which check spoke. An omitted net= defaults to
// false — no network, matching the runner's fail-closed posture.
func ParsePolicy(raw string) (Policy, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Policy{}, "empty"
	}

	// Split the value at cmd= so the command is captured whole.
	var head, cmdVal string
	cmdSeen := false
	switch {
	case strings.HasPrefix(raw, "cmd="):
		cmdVal, cmdSeen = raw[len("cmd="):], true
	case strings.Contains(raw, ",cmd="):
		i := strings.Index(raw, ",cmd=")
		head, cmdVal, cmdSeen = raw[:i], raw[i+len(",cmd="):], true
	}
	if !cmdSeen {
		return Policy{}, "no cmd= (a policy with no command would report a result for a check that never ran)"
	}
	if stray := strayFieldAfterCmd(cmdVal); stray != "" {
		return Policy{}, stray + "= appears after cmd=, which takes the rest of the value verbatim; move it before cmd="
	}
	// TrimSpace, NOT strings.Fields. Splitting the command into fields and
	// rejoining them with spaces destroyed newlines and would have mangled
	// quoted arguments: "true # comment\nfalse" collapsed onto one line and
	// the failing step vanished behind the comment. The command is one string
	// from here to the jail.
	cmd := strings.TrimSpace(cmdVal)
	if cmd == "" {
		return Policy{}, "cmd= is empty"
	}

	pol := Policy{CheckCmd: cmd}
	var repoSeen bool
	for _, kv := range strings.Split(head, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return Policy{}, "field " + strconv.Quote(kv) + " is not key=value"
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
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
			n, err := strconv.Atoi(val)
			switch {
			case err != nil:
				return Policy{}, "timeout=" + val + " is not a number"
			case n < 0:
				return Policy{}, "timeout=" + val + " is negative"
			case n > maxGateTimeoutS:
				return Policy{}, "timeout=" + val + "s exceeds the maximum " + strconv.Itoa(maxGateTimeoutS) + "s"
			default:
				pol.TimeoutS = n
			}
		default:
			return Policy{}, "unknown field " + strconv.Quote(key) + " (known: " + strings.Join(policyFields, ", ") + ", cmd)"
		}
	}
	if !repoSeen {
		return Policy{}, "no repo="
	}
	return pol, ""
}

// strayFieldAfterCmd returns the name of a known policy field that appears
// after cmd= (where it would be swallowed into the command verbatim), or "".
func strayFieldAfterCmd(cmdVal string) string {
	for _, f := range policyFields {
		if strayFieldRE[f].MatchString(cmdVal) {
			return f
		}
	}
	return ""
}
