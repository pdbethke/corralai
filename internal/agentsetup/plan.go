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
	// ActionForget drops setup's record of a registration that is already gone
	// (removed by hand), so a later registration with the same command is never
	// mistaken for setup's. It touches only the state file (uninstall).
	ActionForget ActionKind = "forget"
)

// Action is one step. Setup builds a Plan of them and prints it before any is
// applied, so the operator sees exactly what will be written.
type Action struct {
	AgentID string
	Kind    ActionKind
	Argv    []string // register/replace/enable/remove
	Path    string   // write-skill/write-file/remove-skill/unwrite-file
	Note    string   // why, e.g. "will replace: <old> → <new>"
	// Settled marks a noop that is a satisfied end state (already registered,
	// wired by the plugin, nothing of setup's to remove). A noop that is not
	// Settled is Blocked: setup could not do or see what was asked (not
	// installed, could not read). Callers decide exit codes from this, never
	// from Note's wording.
	Settled bool
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

// SameCommand compares a registered argv to [bin, "mcp"]. Text parsers split a
// binary path containing spaces, so equality of the space-joined forms counts.
func SameCommand(got []string, bin string) bool {
	if len(got) == 2 && got[0] == bin && got[1] == "mcp" {
		return true
	}
	return strings.Join(got, " ") == bin+" mcp"
}

// BuildSetupPlan decides, per selected agent, what setup WILL do. It reads
// statuses (and, to name the backup a config-file edit will make, that file)
// and writes nothing. An ID in selected that is not an agent is an
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
		settled := func(note string) {
			acts = append(acts, Action{AgentID: a.ID, Kind: ActionNoop, Note: note, Settled: true})
		}
		switch {
		case !s.Installed:
			add(ActionNoop, nil, "", "not installed")
			p.Actions = append(p.Actions, acts...)
			continue
		case s.ViaPlugin:
			settled("wired by the corral plugin")
			p.Actions = append(p.Actions, tag(a, acts)...)
			continue
		case s.Unknown != "":
			add(ActionNoop, nil, "", "agent state could not be read, so setup leaves it alone: "+s.Unknown)
			p.Actions = append(p.Actions, tag(a, acts)...)
			continue
		}

		regs := a.Register(opt.Bin)
		switch {
		case s.Registered && SameCommand(s.Command, opt.Bin):
			settled("already registered: " + opt.Bin + " mcp")
		case s.Registered:
			old := "(command unknown)"
			if len(s.Command) > 0 {
				old = strings.Join(s.Command, " ")
			}
			note := fmt.Sprintf("will replace: %s → %s mcp", old, opt.Bin)
			if a.FileConfig != "" {
				path := a.File.Path(opt.Home)
				add(ActionWriteFile, nil, path, note+"; "+backupNote(path))
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
				path := a.File.Path(opt.Home)
				add(ActionWriteFile, nil, path, "add corral to the config file; "+backupNote(path))
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
