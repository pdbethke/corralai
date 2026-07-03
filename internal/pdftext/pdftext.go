// SPDX-License-Identifier: Elastic-2.0

// Package pdftext extracts plain text from PDF bytes — pure Go (no pdftotext or
// other system tools), so it stays portable and links into both the brain and
// the thin corral-admin client (no CGO, no DuckDB).
package pdftext

import (
	"bytes"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// IsPDF reports whether data looks like a PDF (the %PDF- magic header).
func IsPDF(data []byte) bool {
	return bytes.HasPrefix(data, []byte("%PDF-"))
}

// Extract returns the plain text of a PDF document.
func Extract(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	tr, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if _, err := io.Copy(&sb, tr); err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}
