// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/skills"
)

// Result is one agent's outcome. Detail carries the CLI's output (or the
// error) on failure, so the operator sees the agent's own words.
type Result struct {
	AgentID string
	OK      bool
	Detail  string
}

// Apply carries out a printed plan agent by agent, in plan order, and records
// in st exactly what it added so uninstall can remove only that.
//
// One agent failing never stops the others. Within an agent the first failed
// action stops that agent: a `remove` that precedes a `replace` must succeed
// before the replace runs, because replacing over a registration the agent
// refused to drop would leave it in an unknown state. State is saved to
// statePath after each agent finishes, so a crash mid-apply leaves an accurate
// record of everything completed so far.
//
// bin is the absolute corral path being registered (the config-file agents
// have no argv to carry it) and version is written into skill markers. Neither
// has a default: the caller names them.
//
// A plan with registrations (setup) records Command, RegisterArgv and
// SkillDir. A plan with removals (uninstall) clears what each success undid
// and drops an agent's record once its registration is gone.
func Apply(ctx context.Context, r Runner, p Plan, home, statePath, bin, version string, st *State) []Result {
	if st.Agents == nil {
		st.Agents = map[string]AgentRecord{}
	}
	var order []string
	groups := map[string][]Action{}
	for _, a := range p.Actions {
		if _, ok := groups[a.AgentID]; !ok {
			order = append(order, a.AgentID)
		}
		groups[a.AgentID] = append(groups[a.AgentID], a)
	}
	var results []Result
	for _, id := range order {
		// An interrupt (ctx canceled by Ctrl-C) starts no new agent: the
		// person asked setup to stop, so nothing more of theirs is edited.
		if ctx.Err() != nil {
			results = append(results, Result{AgentID: id, Detail: "interrupted before this agent was started; nothing was changed in it"})
			continue
		}
		res := applyAgent(ctx, r, id, groups[id], bin, version, st)
		// A failed save is reported either way: an agent that already failed
		// may still have changed something (a remove before a failed replace),
		// and the person must learn that the record of it is stale.
		if err := SaveState(statePath, *st); err != nil {
			if res.OK {
				res = Result{AgentID: id, Detail: "carried out, but the setup state could not be saved: " + err.Error()}
			} else {
				res.Detail += "\nand the setup state could not be saved either, so its record of this agent may be stale: " + err.Error()
			}
		}
		results = append(results, res)
	}
	return results
}

func applyAgent(ctx context.Context, r Runner, id string, acts []Action, bin, version string, st *State) Result {
	installing, removing := false, false
	removesTotal, removesDone := 0, 0
	for _, a := range acts {
		switch a.Kind {
		case ActionRegister, ActionReplace, ActionEnable, ActionWriteFile:
			installing = true
		case ActionRemove, ActionUnwriteFile, ActionRemoveSkill, ActionForget:
			removing = true
		}
		if a.Kind == ActionRemove {
			removesTotal++
		}
	}
	unregistered := false // a remove succeeded: the agent no longer has corral registered
	rec, had := st.Agents[id]
	fail := func(detail string) Result { return Result{AgentID: id, Detail: detail} }
	changed := false
	resetArgv := true
	command := []string{bin, "mcp"}

	run := func(argv []string) error {
		out, err := r.Run(ctx, argv)
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	recordArgv := func(argv []string) {
		if resetArgv {
			rec.RegisterArgv, resetArgv = nil, false
		}
		rec.RegisterArgv = append(rec.RegisterArgv, argv)
		rec.Command = command
		changed = true
	}

	for _, a := range acts {
		var err error
		// The file and skill steps never reach the Runner, so they would not
		// notice an interrupt on their own: every step that changes anything
		// checks ctx before it starts, and a step not started is not done.
		if a.Kind != ActionNoop && a.Kind != ActionSkipSkill && ctx.Err() != nil {
			err = fmt.Errorf("interrupted before %s; it and the steps after it were not started", a.Kind)
		}
		switch {
		case err != nil: // interrupted: nothing is started
		case a.Kind == ActionRegister, a.Kind == ActionReplace, a.Kind == ActionEnable:
			if err = run(a.Argv); err == nil {
				recordArgv(a.Argv)
			}
		case a.Kind == ActionRemove:
			err = run(a.Argv)
			if err == nil {
				removesDone++
				unregistered = true
				// Uninstall: the record is cleared only once ALL of the agent's
				// removes have succeeded, so a failure part way keeps what setup
				// can still retry. Before a replace the registration is really
				// gone after the first remove, so the record must say so even if
				// the replace then fails.
				if installing || removesDone == removesTotal {
					rec.Command, rec.RegisterArgv, changed = nil, nil, true
				}
			}
		case a.Kind == ActionWriteFile:
			err = writeServerFile(a, id, bin)
			if err == nil {
				rec.Command, changed = command, true
			}
		case a.Kind == ActionUnwriteFile:
			err = unwriteServerFile(a, id)
			if err == nil {
				rec.Command, rec.RegisterArgv, changed = nil, nil, true
			}
		case a.Kind == ActionWriteSkill:
			var files map[string][]byte
			if files, err = skills.Corral(); err == nil {
				err = WriteSkill(a.Path, version, files)
			}
			if err == nil {
				rec.SkillDir, changed = a.Path, true
			}
		case a.Kind == ActionRemoveSkill:
			if err = RemoveSkill(a.Path); err == nil {
				rec.SkillDir, changed = "", true
			}
		case a.Kind == ActionForget:
			rec.Command, rec.RegisterArgv, changed = nil, nil, true
		}
		if err != nil {
			if changed {
				store(st, id, rec) // keep what did succeed before this step
			}
			detail := err.Error()
			if unregistered && installing {
				detail += "\ncorral is now UNREGISTERED from this agent: the old registration was removed and the new one was not added"
			}
			return fail(detail)
		}
	}
	switch {
	case removing && !installing && len(rec.Command) == 0 && (changed || had):
		delete(st.Agents, id)
	case changed:
		store(st, id, rec)
	}
	return Result{AgentID: id, OK: true}
}

// store keeps rec, or drops the agent when nothing of setup's remains in it.
func store(st *State, id string, rec AgentRecord) {
	if len(rec.Command) == 0 && len(rec.RegisterArgv) == 0 && rec.SkillDir == "" {
		delete(st.Agents, id)
		return
	}
	st.Agents[id] = rec
}

func serverFile(a Action, id string) (Agent, error) {
	ag, ok := ByID(id)
	if !ok || ag.File == nil || a.Path == "" {
		return Agent{}, fmt.Errorf("agent %q has no config file to edit", id)
	}
	return ag, nil
}

func writeServerFile(a Action, id, bin string) error {
	ag, err := serverFile(a, id)
	if err != nil {
		return err
	}
	return editConfigFile(a.Path, func(old []byte) ([]byte, error) {
		return MergeServerConfig(old, ag.File.ServersKey, bin)
	})
}

func unwriteServerFile(a Action, id string) error {
	ag, err := serverFile(a, id)
	if err != nil {
		return err
	}
	return editConfigFile(a.Path, func(old []byte) ([]byte, error) {
		return UnmergeServerConfig(old, ag.File.ServersKey)
	})
}

// editConfigFile rewrites an agent's config file through edit. The pre-edit
// bytes are first copied byte-exact to <path>.corral.backup (0o600); a backup
// that already exists is the FIRST pre-edit state and is kept, the newer bytes
// going to <path>.corral.backup.<sha256-12> instead. The file is then replaced
// through a temp file and rename, mode 0o600. A missing file is created by an
// edit that produces content, and left missing by one that produces none.
// Unchanged output writes nothing and makes no backup.
func editConfigFile(path string, edit func(old []byte) ([]byte, error)) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; setup will not edit a symlinked config file (edit its target yourself, or replace the link with a file)", path)
	}
	old, err := os.ReadFile(path) // #nosec G304 -- the agent table's config path
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	next, err := edit(old)
	if err != nil {
		return err
	}
	if exists && bytes.Equal(next, old) || !exists && len(next) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if exists {
		bk := backupPathFor(path, old)
		if err := writeAtomic(bk, old); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	}
	return writeAtomic(path, next)
}

// backupPathFor is where editConfigFile backs up old, the current bytes of
// path: <path>.corral.backup, or, when that FIRST backup already exists (it is
// kept), <path>.corral.backup.<sha256-12 of old>.
func backupPathFor(path string, old []byte) string {
	bk := path + ".corral.backup"
	if _, err := os.Lstat(bk); err == nil {
		h := sha256.Sum256(old)
		bk += "." + hex.EncodeToString(h[:])[:12]
	}
	return bk
}

// BackupPath names the backup an edit of the config file at path would make
// now, so the printed plan can name it before anything is written. "" with no
// error means the file does not exist, so no backup is made.
func BackupPath(path string) (string, error) {
	old, err := os.ReadFile(path) // #nosec G304 -- the agent table's config path
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return backupPathFor(path, old), nil
}

// backupNote is the plan's wording for BackupPath.
func backupNote(path string) string {
	bk, err := BackupPath(path)
	switch {
	case err != nil:
		return "backup: could not be named (" + err.Error() + ")"
	case bk == "":
		return "no backup (the file does not exist yet)"
	}
	return "backup: " + bk
}
