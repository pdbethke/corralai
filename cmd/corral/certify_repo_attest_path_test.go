// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A receipt path whose directory does not exist yet is made, and a path
// that cannot be written refuses before anything is spent. Seen on camera:
// `--attest .corral/flask-statement.json` on a checkout with no `.corral/`
// spent the herd's budget and then failed to write the statement.
func TestAttestPathIsMadeAndProvedWritableBeforeTheRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not", "yet", "statement.json")
	if err := preflightWritable(p); err != nil {
		t.Fatalf("a missing directory must be made: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".probe"); err == nil {
		t.Fatal("the probe file must be removed")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if err := preflightWritable(filepath.Join(ro, "statement.json")); err == nil {
		t.Fatal("an unwritable path must be refused")
	}
}
