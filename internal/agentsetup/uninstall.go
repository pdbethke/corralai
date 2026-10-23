// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import "strings"

// BuildUninstallPlan decides, per selected agent, what removing corral WILL do.
// It reads statuses and the state file and writes nothing.
//
// Setup removes only what setup recorded adding: an agent is touched only when
// State has a record with a Command AND the agent still reports that same
// command. A registration the operator made by hand, or one they have since
// changed, is theirs and gets a noop with the reason.
//
// Agents with a remove command get ActionRemove; agents whose only channel is a
// config file (cursor, opencode, vscode) get ActionUnwriteFile on that file.
//
// The skill copy is removed only when it is setup's own unedited copy AND every
// agent State lists as using the directory is leaving in THIS plan (its
// registration is being removed, or it never had one). That is decided in a
// second pass, after every agent's registration outcome is known: an agent
// whose uninstall is a noop (unreadable, changed, not installed) keeps its
// record, and with it its claim on a shared directory, whatever the order the
// agents were listed in.
// Unselected IDs are ignored (unknown IDs are the caller's to reject).
func BuildUninstallPlan(stat []Status, selected []string, st State, home string) Plan {
	want := map[string]bool{}
	for _, id := range selected {
		want[id] = true
	}
	var p Plan
	leaving := map[string]bool{}
	for _, s := range stat {
		a := s.Agent
		if !want[a.ID] {
			continue
		}
		add := func(k ActionKind, argv []string, path, note string) {
			p.Actions = append(p.Actions, Action{AgentID: a.ID, Kind: k, Argv: argv, Path: path, Note: note})
		}
		settled := func(note string) {
			p.Actions = append(p.Actions, Action{AgentID: a.ID, Kind: ActionNoop, Note: note, Settled: true})
		}
		rec, have := st.Agents[a.ID]
		switch {
		case !have:
			settled("not added by setup, so setup leaves it alone")
		case !s.Installed:
			settled("not installed; nothing removed")
		case s.Unknown != "":
			add(ActionNoop, nil, "", "could not read; nothing removed: "+s.Unknown)
		case len(rec.Command) == 0:
			leaving[a.ID] = true // nothing registered by setup; only a skill claim
			settled("the registration was not added by setup, so setup leaves it alone")
		case !s.Registered:
			settled("corral is no longer registered; nothing to remove")
		case len(rec.Command) != 2 || rec.Command[1] != "mcp" || !SameCommand(s.Command, rec.Command[0]):
			settled("the registration changed since setup (" + strings.Join(s.Command, " ") + "), so setup leaves it alone")
		case len(a.Remove) > 0:
			leaving[a.ID] = true
			for _, rm := range a.Remove {
				add(ActionRemove, rm, "", "")
			}
		case a.File != nil:
			leaving[a.ID] = true
			add(ActionUnwriteFile, nil, a.File.Path(home), "delete only the corral entry")
		default:
			add(ActionNoop, nil, "", "the agent has no remove command")
		}
	}

	skillDirs := map[string]bool{}
	for _, s := range stat {
		a := s.Agent
		dir := st.Agents[a.ID].SkillDir
		if !want[a.ID] || dir == "" || skillDirs[dir] {
			continue
		}
		skillDirs[dir] = true
		var staying []string
		for _, u := range st.SkillUsers(dir) {
			if !leaving[u] {
				staying = append(staying, u)
			}
		}
		act := func(k ActionKind, note string) {
			p.Actions = append(p.Actions, Action{AgentID: a.ID, Kind: k, Path: dir, Note: note})
		}
		switch {
		case len(staying) > 0:
			act(ActionSkipSkill, "skill: "+dir+" is still used by "+strings.Join(staying, ", ")+", kept")
		case s.Skill != "managed":
			act(ActionSkipSkill, "skill: "+dir+" is not setup's unedited copy, left alone")
		default:
			act(ActionRemoveSkill, "")
		}
	}
	return p
}
