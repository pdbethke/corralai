// SPDX-License-Identifier: Elastic-2.0

package adequacy

import "strings"

// Overlay is the workspace one run sees: Base, with code placed at CodePath
// through Compose. The one place a run's files are assembled: every grading
// run (baseline, mutant, canary, compile check, enumeration) builds its map
// here, so a rule about how the code lands in the file reaches all of them
// and cannot be remembered at nine doors and forgotten at the tenth.
// TestNoWorkspaceMapIsAssembledOutsideOverlay fails a hand-built one.
type Overlay struct {
	Base     map[string]string
	CodePath string
	// Compose turns the code in place into the file written at CodePath;
	// nil writes the code as is.
	Compose func(code string) string
}

// With returns a fresh map: Base with code (through Compose) at CodePath.
// Base is never modified, so the same Base serves every run.
func (o Overlay) With(code string) map[string]string {
	files := make(map[string]string, len(o.Base)+1)
	for k, v := range o.Base {
		files[k] = v
	}
	if o.Compose != nil {
		code = o.Compose(code)
	}
	files[o.CodePath] = code
	return files
}

// AppendBlock is the inline rule: Compose for a run whose authored test
// lands IN the audited file itself (a Rust source's own `mod tests`, the
// test reaching the code with `use super::*`). The block is appended to
// whatever code is in place (original, mutant or canary), on a line of its
// own: a newline is put between them only when the code does not already
// end in one.
func AppendBlock(block string) func(code string) string {
	return func(code string) string {
		if code != "" && !strings.HasSuffix(code, "\n") {
			return code + "\n" + block
		}
		return code + block
	}
}
