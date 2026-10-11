//go:build !cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import "github.com/pdbethke/corralai/internal/pairing"

func extractSignatures(_, _ string) ([]Signature, error) { return nil, ErrUnsupportedLang }

// testModules: no grammar without cgo, so no module is ever read.
func testModules(_ string, _ []byte) []pairing.TestModule { return nil }
