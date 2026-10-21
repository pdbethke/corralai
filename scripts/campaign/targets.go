// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Seats names the model (or `agent:model`) behind each role of one audit.
// Every seat is required except Derive, and Derive only when Goals is set:
// corral has no default models, and a campaign that quietly picked one would
// be the exception to that rule living in a script. A missing seat is an error
// naming its key, before any jail, store or spend.
type Seats struct {
	Mutant       string `json:"mutant"`
	Writer       string `json:"writer"`
	ShadowWriter string `json:"shadow_writer"`
	Critic       string `json:"critic"`
	Derive       string `json:"derive"` // "" only when Goals is set
}

// Target is one upstream repository the campaign audits. Everything the run
// needs is named here and nothing is defaulted, for the same reason as Seats:
// a default is some one target's value silently imposed on the rest.
type Target struct {
	Name        string   `json:"name"`         // short id, e.g. "starlette"; unique
	Upstream    string   `json:"upstream"`     // "Kludex/starlette"
	Fork        string   `json:"fork"`         // "pdbethke/starlette"
	Branch      string   `json:"branch"`       // the upstream branch to audit
	Path        string   `json:"path"`         // --path scope inside the repo
	Goals       string   `json:"goals"`        // goals JSON, relative to targets.json; "" means derive
	TestCommand []string `json:"test_command"` // argv after "--"
	Substrate   string   `json:"substrate"`    // "jail" or "workspace"
	Jail        string   `json:"jail"`         // jail substrate only: "bwrap" or "container"
	Image       string   `json:"image"`        // jail "container" only: the toolchain image, CORRALAI_EXEC_IMAGE
	Seats       Seats    `json:"seats"`
	MaxTokens   int      `json:"max_tokens"` // > 0
	Timeout     string   `json:"timeout"`    // a Go duration, passed to --timeout
}

// Validate reports the first rule the target breaks, naming the JSON key so
// the operator can find it in targets.json. It runs on every target at load,
// so a bad file is refused before the first audit starts rather than after the
// third has already spent money.
func (t Target) Validate() error {
	required := []struct {
		key string
		ok  bool
	}{
		{"name", strings.TrimSpace(t.Name) != ""},
		{"upstream", strings.TrimSpace(t.Upstream) != ""},
		{"fork", strings.TrimSpace(t.Fork) != ""},
		{"branch", strings.TrimSpace(t.Branch) != ""},
		{"path", strings.TrimSpace(t.Path) != ""},
		{"test_command", len(t.TestCommand) > 0},
		{"substrate", strings.TrimSpace(t.Substrate) != ""},
		{"seats.mutant", strings.TrimSpace(t.Seats.Mutant) != ""},
		{"seats.writer", strings.TrimSpace(t.Seats.Writer) != ""},
		{"seats.shadow_writer", strings.TrimSpace(t.Seats.ShadowWriter) != ""},
		{"seats.critic", strings.TrimSpace(t.Seats.Critic) != ""},
		{"max_tokens", t.MaxTokens > 0},
		{"timeout", strings.TrimSpace(t.Timeout) != ""},
	}
	for _, r := range required {
		if !r.ok {
			return fmt.Errorf("target %q: %s is required", t.Name, r.key)
		}
	}
	// A fork that is its own upstream would make `gh repo sync` and the ledger
	// pushes act on the upstream project itself.
	if strings.EqualFold(strings.TrimSpace(t.Fork), strings.TrimSpace(t.Upstream)) {
		return fmt.Errorf("target %q: fork and upstream are both %q; the fork must be the founder's own repository, never the upstream", t.Name, t.Fork)
	}
	if t.Substrate != "jail" && t.Substrate != "workspace" {
		return fmt.Errorf("target %q: substrate %q must be \"jail\" or \"workspace\"", t.Name, t.Substrate)
	}
	// Which sandbox the jail substrate runs in is named, never defaulted:
	// it decides which toolchain the suite runs with, and corral records it
	// on every entry. A container needs its image and nothing else takes one.
	switch {
	case t.Substrate == "workspace" && (t.Jail != "" || t.Image != ""):
		return fmt.Errorf("target %q: substrate workspace runs no sandbox, so jail and image must be empty", t.Name)
	case t.Substrate == "jail" && t.Jail != "bwrap" && t.Jail != "container":
		return fmt.Errorf("target %q: jail %q must be \"bwrap\" or \"container\" on the jail substrate", t.Name, t.Jail)
	case t.Jail == "container" && strings.TrimSpace(t.Image) == "":
		return fmt.Errorf("target %q: jail container needs an image carrying the project's toolchain", t.Name)
	case t.Jail != "container" && t.Image != "":
		return fmt.Errorf("target %q: image %q is only for jail container", t.Name, t.Image)
	}
	if _, err := time.ParseDuration(t.Timeout); err != nil {
		return fmt.Errorf("target %q: timeout %q is not a Go duration: %w", t.Name, t.Timeout, err)
	}
	if t.Goals == "" && strings.TrimSpace(t.Seats.Derive) == "" {
		return fmt.Errorf("target %q: seats.derive is required when goals is empty (goals are derived by that seat)", t.Name)
	}
	// The decorrelation rule corral enforces at run time (critic != writer),
	// checked here so a bad pairing costs nothing instead of a refused run.
	if t.Seats.Critic == t.Seats.Writer {
		return fmt.Errorf("target %q: seats.critic and seats.writer are both %q; the critic must be a different model", t.Name, t.Seats.Critic)
	}
	// The shadow comparison is only a comparison between two distinct writers.
	if t.Seats.ShadowWriter == t.Seats.Writer {
		return fmt.Errorf("target %q: seats.shadow_writer and seats.writer are both %q; the comparison needs two different writers", t.Name, t.Seats.Writer)
	}
	return nil
}

// ErrInvalid marks a targets file that was read but is wrong (bad JSON, an
// unknown key, a missing field, a duplicate name). Callers tell it from an I/O
// failure with errors.Is, so a bad file exits 2 ("fix what you passed") and an
// unreadable one exits 1.
var ErrInvalid = errors.New("invalid targets file")

// LoadTargets reads and validates a targets file. Unknown keys are an error:
// a misspelled `testcomand` would otherwise decode to an empty command and
// surface as the wrong complaint, or as nothing at all.
func LoadTargets(path string) ([]Target, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the targets file
	if err != nil {
		return nil, err
	}
	var file struct {
		Targets []Target `json:"targets"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: %s: unexpected data after the JSON object", ErrInvalid, path)
	}
	seen := map[string]bool{}
	for _, t := range file.Targets {
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("%w: %s: duplicate target name %q", ErrInvalid, path, t.Name)
		}
		seen[t.Name] = true
	}
	return file.Targets, nil
}
