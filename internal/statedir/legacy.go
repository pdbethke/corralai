// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"fmt"
	"os"
	"path/filepath"
)

// This file is the ONLY production Go file allowed to name the directory
// corral used to keep its state in. It exists for the move and nothing
// else: the refusal below, and `corral state migrate`. Nothing here reads
// data from the old location.
//
// It is deleted, with the refusal and the migrate command, in the first
// release after 1.0 (spec §3, "Removal"). Deleting this file deletes the
// refusal with it, by construction.

// LegacyDir is the directory corral wrote its state to before statedir.
func LegacyDir() (string, error) {
	h, err := host.home()
	if err != nil || h == "" {
		return "", fmt.Errorf("no home directory to find the legacy state in: %v", err)
	}
	return filepath.Join(h, ".claude"), nil
}

// LegacyPath is where row f used to live. Names did not change in the move.
func LegacyPath(f File) (string, error) {
	d, err := LegacyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, f.Name), nil
}

// LegacyError refuses a State row whose only copy is at the legacy path.
// Without it, both key loaders (buildstore.LoadOrCreateSigningKey,
// attest.LoadOrCreateKey) would mint a NEW key beside the old one, and
// every ledger entry signed afterwards would fail against LEDGER_PUBKEY.
type LegacyError struct {
	File   File
	Legacy string
	Want   string
}

func (e *LegacyError) Error() string {
	return fmt.Sprintf("%s exists at %s but not at %s — run `corral state migrate` (or set %s to keep it where it is)",
		e.File.Name, e.Legacy, e.Want, e.File.Override)
}

func refuseLegacyOnly(f File, p string) error {
	if f.Root != State {
		return nil // rebuildable; a fresh cache costs a re-measure, never history
	}
	if _, err := os.Lstat(p); err == nil || !os.IsNotExist(err) {
		return nil
	}
	old, err := LegacyPath(f)
	if err != nil || old == p {
		return nil
	}
	if _, err := os.Lstat(old); err == nil {
		return &LegacyError{File: f, Legacy: old, Want: p}
	}
	return nil
}
