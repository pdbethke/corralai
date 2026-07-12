// SPDX-License-Identifier: Elastic-2.0

package controlgate

import "strings"

// ControlPolicy binds one control owner to one repo's control gate. Repo is
// "owner/name" (GitHub, matching gate.Policy.Repo). Owner is the control-owner
// principal ListVetted is keyed on. Lang selects the built-in workspace
// scaffold (LangScaffold). Base is the target branch ("" = all bases).
type ControlPolicy struct {
	Repo  string
	Base  string
	Owner string
	Lang  string
}

// ParseControlPolicies parses CORRALAI_CONTROL_GATE: ";"-separated entries,
// each ","-separated key=value pairs — "repo=o/r,owner=lead@x,lang=go,base=main".
// An empty raw string yields (nil,nil): the feature's off switch. An entry
// missing repo= or owner=, or naming an unknown lang, is skipped and reported
// in bad (degrade-never-block — one bad entry must not disable the others).
// Omitted lang defaults to "go".
func ParseControlPolicies(raw string) (policies []ControlPolicy, bad []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		pol := ControlPolicy{Lang: "go"}
		for _, kv := range strings.Split(entry, ",") {
			key, val, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if !ok {
				continue
			}
			key, val = strings.TrimSpace(key), strings.TrimSpace(val)
			switch key {
			case "repo":
				pol.Repo = val
			case "owner":
				pol.Owner = val
			case "lang":
				if val != "" {
					pol.Lang = val
				}
			case "base":
				pol.Base = val
			}
		}
		if _, _, ok := LangScaffold(pol.Lang); pol.Repo == "" || pol.Owner == "" || !ok {
			bad = append(bad, entry)
			continue
		}
		policies = append(policies, pol)
	}
	return policies, bad
}

// LangScaffold returns the minimal workspace (base file set + test command)
// a vetted test for lang re-runs inside. v1 supports Go; unknown → !ok, which
// ParseControlPolicies rejects loudly. The scaffold MUST match the one the
// test was authored/vetted against (package name, module path).
func LangScaffold(lang string) (base map[string]string, testCmd []string, ok bool) {
	switch lang {
	case "go":
		return map[string]string{"go.mod": "module control\ngo 1.26\n"}, []string{"go", "test", "./"}, true
	}
	return nil, nil, false
}
