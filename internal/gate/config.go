// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"fmt"
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
// sides of the separator and the '='. It used to match only a comma followed
// by the lowercase name, so "cmd=true,Base=release" and "cmd=true\nbase=release"
// were swallowed into the command and the policy gated every base while the
// operator had named one — the outcome this guard exists to report. (Review
// 8be2189163b0, R6.) It now matches:
//
//   - after a COMMA, in ANY case. No shell line starts with ",Base=", so a
//     comma-led field name is a misplaced field whatever its case.
//   - at the start of a LINE, in LOWERCASE only — the spelling a policy field
//     is written in. A multi-line script legitimately assigns shell variables
//     on their own lines, and an uppercase BASE= or TIMEOUT= there is the
//     script's, not the policy's; matching those too refused working policies
//     on upgrade, which an adversarial review of this fix caught before merge.
//
// Two errors are accepted on purpose, and both are LOUD — the policy is
// refused and named — where the miss this guards against was silent and
// failed OPEN: a quoted ",base=" inside a legitimate command, and a lowercase
// shell assignment such as "timeout=30" on its own line. The operator rewords
// the command; nothing is gated against the wrong base.
var strayFieldRE = func() map[string]*regexp.Regexp {
	m := make(map[string]*regexp.Regexp, len(policyFields))
	for _, f := range policyFields {
		q := regexp.QuoteMeta(f)
		m[f] = regexp.MustCompile(`(?i:,\s*` + q + `\s*=)|\n[ \t]*` + q + `[ \t]*=`)
	}
	return m
}()

// cmdFieldRE finds the FIRST cmd= that begins a field: at the start of the
// value or after a comma, with the same whitespace tolerance every other
// field gets. It used to be the exact substrings "cmd=" and ",cmd=", so
// "repo=o/r, cmd=true" was refused as having no command and that repo's gate
// was off apart from a log line. (Review 8be2189163b0, R3.)
var cmdFieldRE = regexp.MustCompile(`(^|,)\s*cmd\s*=`)

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

	var accepted []string // variable name of each entry in policies
	for _, f := range found {
		pol, reason := ParsePolicy(f.val)
		if reason != "" {
			bad = append(bad, PolicyEnvPrefix+f.name+": "+reason)
			continue
		}
		// TWO POLICIES MAY NOT ANSWER ONE PULL REQUEST UNDER ONE STATUS. The
		// poller dedupes on (repo, head, context), so the second of two such
		// policies was skipped on every head, forever, while the first one's
		// status stood in for a check that never ran. Refused here, loudly,
		// with the first by name kept — the same deterministic order the
		// poller would have used, now stated instead of silent. (Review of
		// main at 6951ca4c, 2026-09-15, R1 — high, reproduced; ledger entry
		// 8be2189163b0.)
		if i := sharesAStatusWith(policies, pol); i >= 0 {
			bad = append(bad, fmt.Sprintf("%s%s: would report on the same pull requests of %s under the same status context %q as %s%s, and only one of them would ever run — give one a distinct context=",
				PolicyEnvPrefix, f.name, pol.Repo, normalizeContext(pol.Context), PolicyEnvPrefix, accepted[i]))
			continue
		}
		policies = append(policies, pol)
		accepted = append(accepted, f.name)
	}
	return policies, bad
}

// sharesAStatusWith returns the index of the first policy in ps that would
// post p's status on some pull request p also covers, or -1. That takes the
// same repo, the same normalized context, and base sets that can both match
// one pull request: an unset base means every base, so it overlaps any.
func sharesAStatusWith(ps []Policy, p Policy) int {
	ctx := normalizeContext(p.Context)
	for i, q := range ps {
		if q.Repo != p.Repo || normalizeContext(q.Context) != ctx {
			continue
		}
		if basesOverlap(q.Base, p.Base) {
			return i
		}
	}
	return -1
}

func basesOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
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
// false — no network, matching the runner's fail-closed posture — and a net=
// that is not exactly true, false, 1 or 0 is refused, never defaulted.
func ParsePolicy(raw string) (Policy, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Policy{}, "empty"
	}

	// Split the value at cmd= so the command is captured whole.
	var head, cmdVal string
	loc := cmdFieldRE.FindStringIndex(raw)
	if loc != nil {
		head, cmdVal = raw[:loc[0]], raw[loc[1]:]
	}
	if loc == nil {
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
			// Exactly true/1 or false/0; anything else is refused, the rule
			// timeout= follows. It used to map every value but "true" and "1"
			// to no-network, silently, so net=yes produced a gate that failed
			// every network-needing check with nothing pointing at the
			// policy. (Review 8be2189163b0, R4.)
			//
			// NOT strconv.ParseBool: it also accepts "TRUE", "True" and "t",
			// which USED to mean no network — so it would have opened the
			// jail's network to untrusted pull-request code for policies that
			// had it closed. The fix must never widen the jail; its worst
			// case is a logged refusal.
			switch val {
			case "true", "1":
				pol.AllowNet = true
			case "false", "0":
				pol.AllowNet = false
			default:
				return Policy{}, "net=" + val + " is not one of true, false, 1, 0"
			}
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
