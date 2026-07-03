// SPDX-License-Identifier: Elastic-2.0

package pdftext

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// makePDF builds a minimal valid PDF (correct xref offsets) containing text — so
// the test needs no binary fixture.
func makePDF(text string) []byte {
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>",
	}
	content := "BT /F1 24 Tf 72 720 Td (" + text + ") Tj ET"
	objs = append(objs, fmt.Sprintf("<</Length %d>>stream\n%s\nendstream", len(content), content))
	objs = append(objs, "<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>")
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	off := make([]int, len(objs)+1)
	for i, o := range objs {
		off[i+1] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, o))
	}
	x := b.Len()
	b.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objs)+1))
	for i := 1; i <= len(objs); i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString(fmt.Sprintf("trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF", len(objs)+1, x))
	return b.Bytes()
}

func TestIsPDF(t *testing.T) {
	if !IsPDF([]byte("%PDF-1.4\n...")) {
		t.Fatal("should detect a PDF header")
	}
	if IsPDF([]byte("<html>not a pdf")) {
		t.Fatal("HTML is not a PDF")
	}
}

func TestExtract(t *testing.T) {
	want := "Hello RAG world from a PDF"
	got, err := Extract(makePDF(want))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(got, want) {
		t.Fatalf("extracted %q, want it to contain %q", got, want)
	}
}
