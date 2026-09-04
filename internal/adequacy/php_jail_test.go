// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestJailPHPLoadsItsExtensions pins the sixth review's H3: the bwrap jail
// bound /usr but not /etc/php (where Debian/Ubuntu enable every extension)
// nor /etc/alternatives (which the `php` on PATH resolves through), so php
// inside the jail loaded no ini at all — 20 built-in modules, no dom /
// mbstring / tokenizer / xml — and PHPUnit refused to start on every run,
// while the PHP pre-flight (`php -v`, which needs no extension) said the
// jail could run it. Skips where there is no bwrap or no php; runs the
// real jail otherwise.
func TestJailPHPLoadsItsExtensions(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("no bwrap here: %v", err)
	}
	j := NewJail(backend, 60*time.Second)
	vj, ok := j.(VerboseJail)
	if !ok {
		t.Fatal("jail is not verbose")
	}
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("no php on this box")
	}
	// Ask for the list on stdout and drop stderr: on the Hetzner runner the
	// snmp extension, now that it loads, prints hundreds of MIB warnings at
	// startup, which pushed `php -m`'s real output past the jail's output
	// cap and made a passing box look like a failing one.
	_, out, err := vj.RunTestVerbose(context.Background(), map[string]string{"x.php": "<?php echo 'x';"},
		[]string{"sh", "-c", `php -r 'echo "Core\n" . implode("\n", get_loaded_extensions()) . "\n";' 2>/dev/null`})
	if err != nil {
		t.Fatalf("php is on the host PATH but did not run in the jail: %v", err)
	}
	if !strings.Contains(out, "Core") {
		// The host has php and the jail could not run it — on Debian that
		// is the /etc/alternatives symlink the jail did not bind.
		t.Fatalf("php is on the host PATH but produced no module list inside the jail:\n%s", out)
	}
	for _, ext := range []string{"dom", "mbstring", "tokenizer", "xml"} {
		if !strings.Contains(out, "\n"+ext+"\n") {
			t.Errorf("php inside the jail lacks %s — /etc/php is not bound:\n%s", ext, out)
		}
	}
}
