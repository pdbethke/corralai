//go:build !cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	plugins "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

func extractSignatures(_, _ string) ([]Signature, error) { return nil, ErrUnsupportedLang }

// readsTestModules: no grammar without cgo, so no language's modules can be
// read.
func readsTestModules(string) bool { return false }

// testModules: no grammar without cgo, so no module is ever read.
func testModules(_ string, _ []byte) []pairing.TestModule { return nil }

// ownTestLines: no grammar without cgo, so nothing is read. Such a build
// cannot read a Rust file at all and refuses it upstream.
func ownTestLines(string, []byte) ([]plugins.LineRange, string) { return nil, "" }
