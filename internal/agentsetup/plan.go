// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// ActionKind names one step of a setup or uninstall plan.
type ActionKind string

const (
	ActionRegister    ActionKind = "register"     // run Argv: add the server
	ActionReplace     ActionKind = "replace"      // run Argv: add over a different registration
	ActionNoop        ActionKind = "noop"         // nothing to do; Note says why
	ActionWriteSkill  ActionKind = "write-skill"  // copy the skill into Path
	ActionSkipSkill   ActionKind = "skip-skill"   // leave the skill alone; Note says why
	ActionWriteFile   ActionKind = "write-file"   // merge corral into the config file at Path (cursor)
	ActionEnable      ActionKind = "enable"       // run Argv after a file write (cursor)
	ActionRemove      ActionKind = "remove"       // run Argv: remove the server (uninstall, or before a replace)
	ActionRemoveSkill ActionKind = "remove-skill" // delete the managed skill copy at Path (uninstall)
	ActionUnwriteFile ActionKind = "unwrite-file" // delete corral from the config file at Path (uninstall)
)

// Action is one step. Setup builds a Plan of them and prints it before any is
// applied, so the operator sees exactly what will be written.
type Action struct {
	AgentID string
	Kind    ActionKind
	Argv    []string // register/replace/enable/remove
	Path    string   // write-skill/write-file/remove-skill/unwrite-file
	Note    string   // why, e.g. "will replace: <old> → <new>"
}

// Plan is the ordered list of Actions.
type Plan struct{ Actions []Action }

// SetupOptions parameterises BuildSetupPlan.
type SetupOptions struct {
	Bin     string // absolute path of the corral binary to register
	Skill   bool   // also copy the skill
	Home    string
	Version string // build version written into skill markers (used when applying)
}

const unproven = "(not yet proven)"

// sameCommand compares a registered argv to [bin, "mcp"]. Text parsers split a
// binary path containing spaces, so equality of the space-joined forms counts.
func sameCommand(got []string, bin string) bool {
	if len(got) == 2 && got[0] == bin && got[1] == "mcp" {
		return true
	}
	return strings.Join(got, " ") == bin+" mcp"
}

// BuildSetupPlan decides, per selected agent, what setup WILL do. It reads
// statuses and writes nothing. An ID in selected that is not an agent is an
// error. Actions follow the order of st.
//
// Registration: unregistered -> register; registered with [bin, "mcp"] ->
// noop; registered with anything else -> replace (agents whose add refuses a
// duplicate get a preceding remove action). A status that could not be read
// (Unknown) is never acted on, and an agent wired by the corral plugin is left
// alone with no skill action (the plugin ships the skill itself).
//
// Skill: one write-skill per distinct directory, so agents sharing a
// directory do not copy it twice. A copy that is not setup's own is skipped.
func BuildSetupPlan(st []Status, selected []string, opt SetupOptions) (Plan, error) {
	want := map[string]bool{}
	for _, id := range selected {
		if _, ok := ByID(id); !ok {
			return Plan{}, fmt.Errorf("unknown agent %q", id)
		}
		want[id] = true
	}
	var p Plan
	skillDirs := map[string]bool{}
	for _, s := range st {
		a := s.Agent
		if !want[a.ID] {
			continue
		}
		var acts []Action
		add := func(k ActionKind, argv []string, path, note string) {
			acts = append(acts, Action{AgentID: a.ID, Kind: k, Argv: argv, Path: path, Note: note})
		}
		switch {
		case !s.Installed:
			add(ActionNoop, nil, "", "not installed")
			p.Actions = append(p.Actions, acts...)
			continue
		case s.ViaPlugin:
			add(ActionNoop, nil, "", "wired by the corral plugin")
			p.Actions = append(p.Actions, tag(a, acts)...)
			continue
		case s.Unknown != "":
			add(ActionNoop, nil, "", "agent state could not be read, so setup leaves it alone: "+s.Unknown)
			p.Actions = append(p.Actions, tag(a, acts)...)
			continue
		}

		regs := a.Register(opt.Bin)
		switch {
		case s.Registered && sameCommand(s.Command, opt.Bin):
			add(ActionNoop, nil, "", "already registered: "+opt.Bin+" mcp")
		case s.Registered:
			old := "(command unknown)"
			if len(s.Command) > 0 {
				old = strings.Join(s.Command, " ")
			}
			note := fmt.Sprintf("will replace: %s → %s mcp", old, opt.Bin)
			if a.FileConfig != "" {
				add(ActionWriteFile, nil, a.File.Path(opt.Home), note)
				for _, argv := range regs {
					add(ActionEnable, argv, "", "")
				}
				break
			}
			if !a.AddOverwrites {
				for _, rm := range a.Remove {
					add(ActionRemove, rm, "", "the agent's add refuses a duplicate")
				}
			}
			for _, argv := range regs {
				add(ActionReplace, argv, "", note)
			}
		default:
			if a.FileConfig != "" {
				add(ActionWriteFile, nil, a.File.Path(opt.Home), "add corral to the config file")
				for _, argv := range regs {
					add(ActionEnable, argv, "", "")
				}
				break
			}
			for _, argv := range regs {
				add(ActionRegister, argv, "", "")
			}
		}

		// The skill is independent of registration.
		switch {
		case a.SkillMode == SkillNone:
			add(ActionSkipSkill, nil, "", "skill: n/a")
		case !opt.Skill:
			add(ActionSkipSkill, nil, "", "skill: not requested")
		default:
			dir := a.SkillDir(opt.Home)
			if skillDirs[dir] {
				break
			}
			skillDirs[dir] = true
			switch s.Skill {
			case "unmanaged":
				add(ActionSkipSkill, nil, dir, "skill: "+dir+" exists and is not corral setup's copy, left alone")
			case "refused-symlink":
				add(ActionSkipSkill, nil, dir, "skill: "+dir+" is a symlink, left alone")
			case "managed":
				add(ActionWriteSkill, nil, dir, "refresh the skill copy")
			default:
				add(ActionWriteSkill, nil, dir, "")
			}
		}
		p.Actions = append(p.Actions, tag(a, acts)...)
	}
	return p, nil
}

// tag marks every action of an agent no headless load has proven yet.
func tag(a Agent, acts []Action) []Action {
	if a.Proven {
		return acts
	}
	for i := range acts {
		if acts[i].Note == "" {
			acts[i].Note = unproven
		} else {
			acts[i].Note += " " + unproven
		}
	}
	return acts
}

// Render prints one line per action: agent, kind, what, and the note.
func (p Plan) Render(w io.Writer) {
	for _, a := range p.Actions {
		what := strings.Join(a.Argv, " ")
		if what == "" {
			what = a.Path
		}
		if what != "" && a.Path != "" && len(a.Argv) > 0 {
			what += " " + filepath.ToSlash(a.Path)
		}
		line := fmt.Sprintf("  %-12s %-12s", a.AgentID, a.Kind)
		if what != "" {
			line += " " + what
		}
		if a.Note != "" {
			line += "  " + a.Note
		}
		fmt.Fprintln(w, line)
	}
}
