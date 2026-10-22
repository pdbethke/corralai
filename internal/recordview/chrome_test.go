// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"path/filepath"
	"strings"
	"testing"
)

// corralai.dev/ledger/ is where every PR and issue comment points. A site
// build asks for the slot the site's header is copied into, and EVERY page
// has exactly one, the entry pages a link lands on included; a page without
// it would be a dead end, and the site build refuses a page with none.
func TestEveryStaticPageKeepsTheSiteChromeSlot(t *testing.T) {
	s := buildOK(append(manyScans(2), reviewEntry("dddddddddddd", 0)))
	s.SiteChrome = true
	out := t.TempDir()
	if err := WriteStatic(out, s); err != nil {
		t.Fatal(err)
	}
	for _, path := range s.Paths() {
		page := read(t, filepath.Join(out, path))
		if n := strings.Count(page, chromeSlot); n != 1 {
			t.Errorf("%s: %d site chrome slots, want 1", path, n)
		}
		if !strings.Contains(page, "<body>\n"+chromeSlot+"\n") {
			t.Errorf("%s: the slot is not the first thing in <body>, so the header would not span the page", path)
		}
	}
}

// corral ui renders the same pages for one person's own ledger, where the
// public site's header has no business: no slot is left behind.
func TestNoSiteChromeMeansNoSlot(t *testing.T) {
	s := buildOK(manyScans(1))
	out := t.TempDir()
	if err := WriteStatic(out, s); err != nil {
		t.Fatal(err)
	}
	for _, path := range s.Paths() {
		if strings.Contains(read(t, filepath.Join(out, path)), chromeSlot) {
			t.Errorf("%s: the site chrome slot was rendered without SiteChrome", path)
		}
	}
}
