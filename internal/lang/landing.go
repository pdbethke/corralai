// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"

	"github.com/pdbethke/corralai/internal/pairing"
)

// CheckTestLanding refuses a code file whose test corral would write over the
// file itself, and is the ONE place that question is answered.
//
// The pool writes its synthetic and authored tests to
// pairing.Primary(p.TestRules(), codePath) (advpool's advPoolTestPath). For a
// language with an Inline rule (Rust) Primary is the SOURCE: the test belongs
// in the file's own #[cfg(test)] module, which only a test LANDING — merging
// the authored module into the source instead of replacing it — can do
// correctly. Until a language has one, writing to that path would overwrite
// the code under test (on --substrate workspace, a journaled write into the
// operator's real checkout). So the file is refused, never written.
//
// The condition is DERIVED from the rules — Primary names the source — never
// a language name: a language gains a landing by making this false for it,
// not by being removed from a list. Every entry that can start an audit asks
// before any jail, store or spend: advpool.Driver.StartRun, the brain's
// AdvPoolRuntime.StartRun (certify --adversarial and eval reach the pool
// there), certify --local's resolveAuditPlugin (which certify --repo's
// jobs also route through). certify --repo asks earlier still:
// reposcan.Enumerate excludes such a file before pairing, so it is never a
// candidate, a --top slot, a selection language or a reason to prepare a
// jail.
//
// The plugin is the one the extension names, as advPoolTestPath resolves it:
// the check must agree with the path that would actually be written. A file
// no plugin claims is named by Go's rules there, which never name the source,
// so it is not refused here.
func CheckTestLanding(codePath string) error {
	p, ok := Detect(codePath)
	if !ok {
		return nil
	}
	c, ok := pairing.Primary(p.TestRules(), codePath)
	if !ok || filepath.Clean(filepath.FromSlash(c.Path)) != filepath.Clean(filepath.FromSlash(codePath)) {
		return nil
	}
	return fmt.Errorf("%s: corral's %s test landing is not built yet — a test for this file would be written over the file itself, so it cannot be audited (refused before any jail, store or spend)", codePath, p.PromptLang())
}
