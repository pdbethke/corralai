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
// The skill copy is removed only when it is setup's own unedited copy AND no
// agent outside this uninstall still lists the directory in State: several
// agents can share one directory, so removing it for one would break the rest.
// Unselected IDs are ignored (unknown IDs are the caller's to reject).
func BuildUninstallPlan(stat []Status, selected []string, st State, home string) Plan {
	want := map[string]bool{}
	for _, id := range selected {
		want[id] = true
	}
	var p Plan
	skillDirs := map[string]bool{}
	for _, s := range stat {
		a := s.Agent
		if !want[a.ID] {
			continue
		}
		add := func(k ActionKind, argv []string, path, note string) {
			p.Actions = append(p.Actions, Action{AgentID: a.ID, Kind: k, Argv: argv, Path: path, Note: note})
		}
		rec, have := st.Agents[a.ID]
		switch {
		case !have:
			add(ActionNoop, nil, "", "not added by setup, so setup leaves it alone")
			continue
		case !s.Installed:
			add(ActionNoop, nil, "", "not installed; nothing removed")
			continue
		case s.Unknown != "":
			add(ActionNoop, nil, "", "could not read; nothing removed: "+s.Unknown)
			continue
		}

		switch {
		case len(rec.Command) == 0:
			add(ActionNoop, nil, "", "the registration was not added by setup, so setup leaves it alone")
		case !s.Registered:
			add(ActionNoop, nil, "", "corral is no longer registered; nothing to remove")
		case len(rec.Command) != 2 || rec.Command[1] != "mcp" || !sameCommand(s.Command, rec.Command[0]):
			add(ActionNoop, nil, "", "the registration changed since setup ("+strings.Join(s.Command, " ")+"), so setup leaves it alone")
		case len(a.Remove) > 0:
			for _, rm := range a.Remove {
				add(ActionRemove, rm, "", "")
			}
		case a.File != nil:
			add(ActionUnwriteFile, nil, a.File.Path(home), "delete only the corral entry")
		default:
			add(ActionNoop, nil, "", "the agent has no remove command")
		}

		dir := rec.SkillDir
		if dir == "" || skillDirs[dir] {
			continue
		}
		skillDirs[dir] = true
		var others []string
		for _, u := range st.SkillUsers(dir) {
			if !want[u] {
				others = append(others, u)
			}
		}
		switch {
		case len(others) > 0:
			add(ActionSkipSkill, nil, dir, "skill: "+dir+" is still used by "+strings.Join(others, ", ")+", kept")
		case s.Skill != "managed":
			add(ActionSkipSkill, nil, dir, "skill: "+dir+" is not setup's unedited copy, left alone")
		default:
			add(ActionRemoveSkill, nil, dir, "")
		}
	}
	return p
}
