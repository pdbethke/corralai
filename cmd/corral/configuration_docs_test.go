// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/statedir"
)

// The configuration page states store defaults by hand. Each one it states
// must be the table's, so the page cannot drift from statedir.
func TestDocsConfigurationDefaultsMatchStatedir(t *testing.T) {
	b, err := os.ReadFile("../../site/src/content/docs/docs/configuration.mdx")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("(?m)^\\| `(CORRALAI_[A-Z_]+)` \\|[^|]*\\| `([^`]+)` \\|$")
	byVar := map[string]statedir.File{}
	for _, f := range statedir.Files() {
		byVar[f.Override] = f
	}
	checked := 0
	for _, m := range row.FindAllStringSubmatch(string(b), -1) {
		f, ok := byVar[m[1]]
		if !ok {
			continue
		}
		want := "$" + strings.ToUpper(f.Root.String()) + "/" + f.Name
		if m[2] != want {
			t.Errorf("%s: page says %q, statedir says %q", m[1], m[2], want)
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("matched only %d store rows — the regexp no longer reads the table", checked)
	}
}
