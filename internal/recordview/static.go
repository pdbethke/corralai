// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// WriteStatic writes every page of s under out, rendering each through the
// same Render the live Handler uses. Writes go through os.Root,
// the same defence the workspace substrate uses: every path is resolved
// INSIDE out, so a symlink planted in the output directory cannot redirect
// a write anywhere else. 0o750, not 0o755: nothing here needs to be
// world-readable on the machine that builds it; the deploy copies it out.
func WriteStatic(out string, s Site) error {
	p, err := NewPages()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("entry", 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	for _, path := range s.Paths() {
		f, err := root.Create(path)
		if err != nil {
			return err
		}
		if err := p.Render(f, s, path); err != nil {
			f.Close()
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}
