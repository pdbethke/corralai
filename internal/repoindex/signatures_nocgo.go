//go:build !cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

func extractSignatures(_, _ string) ([]Signature, error) { return nil, ErrUnsupportedLang }
