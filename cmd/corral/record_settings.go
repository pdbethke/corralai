// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/creds"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// What a run executed under, as the ledger records it: the test command
// (redacted), the runner row it was read as, and the settings. The entry is
// signed and, for a public repository, published, so this file is the one
// door every such value passes through.

const redactedValue = "<redacted>"

// secretNameParts are substrings that mark a name as a credential. Unlike
// sandbox.ScrubbedEnviron, which deliberately does NOT guess (a project's own
// API key is something its tests need), a false positive here only hides a
// value from a public record and never breaks a run, so the heuristic is the
// safe side to err on.
var secretNameParts = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD"}

// isSecretName reports whether a variable or flag name looks like a
// credential: a name corral knows (creds.CanonicalNames,
// sandbox.CorralOwnSecrets) or one containing a secret part, in any case. A
// flag's dashes read as underscores, so --api-key and API_KEY are one name.
func isSecretName(name string) bool {
	n := strings.ToUpper(strings.ReplaceAll(strings.Trim(name, "-"), "-", "_"))
	if n == "" {
		return false
	}
	for _, known := range creds.CanonicalNames {
		if strings.EqualFold(known, n) {
			return true
		}
	}
	for _, known := range sandbox.CorralOwnSecrets {
		if strings.EqualFold(known, n) {
			return true
		}
	}
	for _, part := range secretNameParts {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

var (
	// NAME=value or --flag=value anywhere in a string: at its start or after
	// any character that cannot be part of a name (a space, ';', '&', '?',
	// a quote), so an assignment inside an `sh -c` script and a query
	// parameter in a URL are both found. The value is a quoted string or
	// the run of characters up to whitespace or a shell separator.
	assignmentRe = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(-{0,2}[A-Za-z_][A-Za-z0-9_.-]*)=("[^"]*"|'[^']*'|[^\s;&|)"']*)`)
	// --flag value inside a script string.
	flagValueRe = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(--?[A-Za-z][A-Za-z0-9_-]*)([ \t]+)("[^"]*"|'[^']*'|[^\s;&|)"'-][^\s;&|)"']*)`)
)

// redactText replaces the value of every secret-named assignment or flag in s.
func redactText(s string) string {
	s = assignmentRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := assignmentRe.FindStringSubmatch(m)
		if !isSecretName(sub[2]) {
			return m
		}
		return sub[1] + sub[2] + "=" + redactedValue
	})
	return flagValueRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := flagValueRe.FindStringSubmatch(m)
		if !isSecretName(sub[2]) || sub[4] == redactedValue {
			return m
		}
		return sub[1] + sub[2] + sub[3] + redactedValue
	})
}

// redactCommand returns argv with every secret value replaced by
// "<redacted>", leaving argv itself untouched. It is fail-safe in the
// direction that matters: a name that merely looks secret loses its value,
// and a value is never let through because its name was unfamiliar. Three
// shapes are covered: NAME=value and --flag=value in any argument (so also
// inside an `sh -c` script), `--flag value` inside a script, and `--flag`
// `value` as two arguments, where the flag's name decides about the next one.
func redactCommand(argv []string) []string {
	out := make([]string, len(argv))
	redactNext := false
	for i, a := range argv {
		if redactNext {
			out[i], redactNext = redactedValue, false
			continue
		}
		out[i] = redactText(a)
		if strings.HasPrefix(a, "-") && !strings.Contains(a, "=") && isSecretName(a) {
			redactNext = true
		}
	}
	return out
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// testCommandText is the recorded text of a test command: argv redacted and
// shell-joined, an argument that needs it single-quoted, so the text reads as
// the command the operator typed. "" for no command.
func testCommandText(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	red := redactCommand(argv)
	parts := make([]string, len(red))
	for i, a := range red {
		if plainArg.MatchString(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// recordedRunner is the runner-table row ID argv is read as under plug: the
// operator's --runner, else the row the command names, else the language's
// default (lang.Resolve, the resolution the evidence pass itself makes).
// "" when plug is nil or no row resolves.
func recordedRunner(plug lang.Plugin, repoDir string, argv []string, runnerFlag string) string {
	if plug == nil {
		return ""
	}
	inv, err := lang.Resolve(plug, repoDir, argv, runnerFlag)
	if err != nil {
		return ""
	}
	r, ok := lang.RunnerFor(plug, inv)
	if !ok {
		return ""
	}
	return r.ID
}

// runSettings are the conditions a run executed under, taken from the values
// the run used, not the flags as typed. Every field is optional because a
// run records only the settings it has: a zero number or empty string is
// ABSENT (no default is invented for a setting the run did not use), and a
// boolean is a pointer so that false, a real value, is recorded.
type runSettings struct {
	Timeout, EvidenceTimeout time.Duration
	SeedLimitBytes           int64
	// Swarm is the resolved swarm width; ScanWorkers the files audited at
	// once, UnitWorkers the units run at once, MutantConcurrency the mutants
	// scored at once within a file, PerFileSwarm the workspace substrate's
	// per-file role width.
	Swarm, ScanWorkers, UnitWorkers, MutantConcurrency, PerFileSwarm int
	BaselineRuns                                                     int
	MaxTokens                                                        int64
	WholeSuite, NoFailFast                                           *bool
	NoGoalCache, NoVerdictCache, NoSelectionCache                    *bool
	WriterMode, Jail, RunnerFlag                                     string
}

func boolPtrOf(b bool) *bool { return &b }

// runSettingsJSON is ScanRow.Settings: the settings as JSON text (a ScanRow
// is compared whole, so no map), "" when the run recorded none. One line per
// key, so a new flag is one line here.
func runSettingsJSON(s runSettings) string {
	m := map[string]any{}
	dur := func(k string, d time.Duration) {
		if d > 0 {
			m[k] = d.String()
		}
	}
	num := func(k string, n int64) {
		if n > 0 {
			m[k] = n
		}
	}
	flag := func(k string, b *bool) {
		if b != nil {
			m[k] = *b
		}
	}
	str := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	dur("timeout", s.Timeout)
	dur("evidence_timeout", s.EvidenceTimeout)
	num("seed_limit_mib", s.SeedLimitBytes>>20)
	num("swarm", int64(s.Swarm))
	num("scan_workers", int64(s.ScanWorkers))
	num("unit_workers", int64(s.UnitWorkers))
	num("mutant_concurrency", int64(s.MutantConcurrency))
	num("per_file_swarm", int64(s.PerFileSwarm))
	num("baseline_runs", int64(s.BaselineRuns))
	num("max_tokens", s.MaxTokens)
	flag("whole_suite", s.WholeSuite)
	flag("no_fail_fast", s.NoFailFast)
	flag("no_goal_cache", s.NoGoalCache)
	flag("no_verdict_cache", s.NoVerdictCache)
	flag("no_selection_cache", s.NoSelectionCache)
	str("writer_mode", s.WriterMode)
	str("jail", s.Jail)
	str("runner_flag", s.RunnerFlag)
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// seedLimitUsed is the jail seed limit the run used: the workspace substrate
// builds no seed, so there is none to record (0 is absent).
func seedLimitUsed(substrate string, limit int64) int64 {
	if substrate == substrateWorkspace {
		return 0
	}
	return limit
}
