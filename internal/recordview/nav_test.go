// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"path/filepath"
	"strings"
	"testing"
)

// corralai.dev/ledger/ is where every PR and issue comment points, and its
// pages had no way back to the site: a maintainer who followed a link could
// not find out what corral is. A site build hands in its menu and EVERY page
// carries it, the entry pages a link lands on included.
func TestEveryStaticPageCarriesTheSiteNav(t *testing.T) {
	s := buildOK(append(manyScans(2), reviewEntry("dddddddddddd", 0)))
	s.Nav = []NavLink{{Label: "What is corral?", Href: "/"}, {Label: "Get started", Href: "/docs/getting-started/"}}
	out := t.TempDir()
	if err := WriteStatic(out, s); err != nil {
		t.Fatal(err)
	}
	for _, path := range s.Paths() {
		page := read(t, filepath.Join(out, path))
		for _, want := range []string{`href="/"`, "What is corral?", `href="/docs/getting-started/"`, "Get started"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: no %q — a reader landing here cannot reach the site", path, want)
			}
		}
		if n := strings.Count(page, `class="sitenav"`); n != 1 {
			t.Errorf("%s: %d site navs, want 1", path, n)
		}
		if strings.Contains(page, navSlot) {
			t.Errorf("%s: the unfilled nav slot reached the page", path)
		}
	}
}

// corral ui renders the same pages for one person's own ledger, where the
// public site's menu has no business: with no Nav, no nav and no slot.
func TestNoNavMeansNoMenu(t *testing.T) {
	s := buildOK(manyScans(1))
	out := t.TempDir()
	if err := WriteStatic(out, s); err != nil {
		t.Fatal(err)
	}
	for _, path := range s.Paths() {
		page := read(t, filepath.Join(out, path))
		if strings.Contains(page, `class="sitenav"`) || strings.Contains(page, navSlot) {
			t.Errorf("%s: a menu (or its slot) was rendered with no Nav", path)
		}
	}
}

// Labels and hrefs are the caller's, so they are escaped like any other value.
func TestNavIsEscaped(t *testing.T) {
	got := navHTML([]NavLink{{Label: `<b>x</b>`, Href: `/a"onmouseover="y`}})
	if strings.Contains(got, "<b>") || strings.Contains(got, `"onmouseover`) {
		t.Fatalf("nav not escaped: %s", got)
	}
}
