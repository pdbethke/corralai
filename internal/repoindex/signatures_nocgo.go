//go:build !cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import "github.com/pdbethke/corralai/internal/pairing"

func extractSignatures(_, _ string) ([]Signature, error) { return nil, ErrUnsupportedLang }

// readsTestModules: no grammar without cgo, so no language's modules can be
// read.
func readsTestModules(string) bool { return false }

// testModules: no grammar without cgo, so no module is ever read.
func testModules(_ string, _ []byte) []pairing.TestModule { return nil }
