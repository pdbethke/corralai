// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"html"
	"strings"
)

// NavLink is one entry of the menu a site build puts on every page (Site.Nav).
type NavLink struct {
	Label string
	Href  string
}

// navSlot marks, in every page template, where Site.Nav goes. Render fills
// it, or removes it when there is no menu, so no template has to thread the
// Site through its own data to draw one.
const navSlot = `<nav data-sitenav></nav>`

// navHTML is the menu itself. Labels and hrefs are the caller's, so both are
// escaped.
func navHTML(links []NavLink) string {
	var b strings.Builder
	b.WriteString(`<nav class="sitenav" aria-label="Site">`)
	for _, l := range links {
		b.WriteString(`<a href="` + html.EscapeString(l.Href) + `">` + html.EscapeString(l.Label) + `</a>`)
	}
	b.WriteString(`</nav>`)
	return b.String()
}

// fillNav puts the menu (or nothing) where the template left navSlot.
func fillNav(page []byte, links []NavLink) []byte {
	with := ""
	if len(links) > 0 {
		with = navHTML(links)
	}
	return bytes.Replace(page, []byte(navSlot), []byte(with), 1)
}
