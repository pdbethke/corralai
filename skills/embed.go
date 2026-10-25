// SPDX-License-Identifier: Elastic-2.0

// Package skills carries the corral Agent Skill inside the binary.
//
// skills/corral is also the Claude Code plugin source, so this file sits one
// level up, in skills/, to keep that folder free of Go. `corral setup` copies
// from here rather than from a checkout, so an installed skill always matches
// the binary that installed it.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed corral
var corralFS embed.FS

// Corral returns every file under skills/corral, keyed by slash-separated
// path relative to that directory (for example "SKILL.md").
func Corral() (map[string][]byte, error) {
	sub, err := fs.Sub(corralFS, "corral")
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		out[p] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the embedded corral skill is empty")
	}
	return out, nil
}
