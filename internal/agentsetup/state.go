// SPDX-License-Identifier: Elastic-2.0

// Package agentsetup wires corral's MCP server and skill into coding agents.
package agentsetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/pdbethke/corralai/internal/statedir"
)

// State is what `corral setup` remembers it did, so undo removes exactly that.
type State struct {
	Agents map[string]AgentRecord `json:"agents"` // keyed by agent ID
}

// AgentRecord is one agent's entry.
type AgentRecord struct {
	RegisterArgv [][]string `json:"register_argv,omitempty"`
	Command      []string   `json:"command,omitempty"` // what was registered: [abs, "mcp"]
	SkillDir     string     `json:"skill_dir,omitempty"`
}

// LoadState reads the state file. A missing file is an empty State; a file
// that exists but cannot be parsed is an error, because treating it as empty
// would let the next save erase what setup had recorded.
func LoadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Agents: map[string]AgentRecord{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("reading setup state %s: %w", path, err)
	}
	if s.Agents == nil {
		s.Agents = map[string]AgentRecord{}
	}
	return s, nil
}

// SaveState writes the state atomically: a temp file in the same directory,
// fsynced, then renamed over the target, mode 0o600.
func SaveState(path string, s State) error {
	if err := statedir.EnsureParent(path); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".setup-state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// SkillUsers returns the IDs of agents whose skill lives in dir, sorted.
// Several agents can share one directory, so removing it is safe only when
// no other agent still lists it. An empty dir matches nothing.
func (s State) SkillUsers(dir string) []string {
	var ids []string
	if dir == "" {
		return ids
	}
	for id, r := range s.Agents {
		if r.SkillDir == dir {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
