// SPDX-License-Identifier: Elastic-2.0

package recordview

import "bytes"

// chromeSlot marks, in every page template (an element, because html/template
// strips comments), where the site's header goes.
// The renderer never draws that header itself: the site build copies it from
// the site's own rendered home page (site/integrations/ledger-chrome.mjs), so
// the ledger cannot drift from the rest of corralai.dev. An earlier menu drawn
// here in Go looked like nothing else on the site.
const chromeSlot = `<div data-site-chrome></div>`

// fillChrome keeps the slot for the site build when keep is set and removes
// it otherwise, so no page carries a marker nobody will fill.
func fillChrome(page []byte, keep bool) []byte {
	if keep {
		return page
	}
	return bytes.Replace(page, []byte(chromeSlot), nil, 1)
}
