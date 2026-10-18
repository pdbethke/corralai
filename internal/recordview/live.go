// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"strings"
)

// Handler serves the record live: every request loads the site afresh (so
// a verdict written from another terminal shows on reload) and renders the
// asked-for page through the same Render that WriteStatic uses. Only
// paths Render knows are served; everything else is 404, so no request
// ever reaches the filesystem by name.
func Handler(load func() (Site, error)) http.Handler {
	p, perr := NewPages()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "the record is read-only here", http.StatusMethodNotAllowed)
			return
		}
		if perr != nil {
			loadFailure(w, perr)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		s, err := load()
		if err != nil {
			loadFailure(w, err)
			return
		}
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			loadFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b.Bytes())
	})
}

var failureTmpl = template.Must(template.New("fail").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>corral — could not read the ledger</title>
<style>` + baseCSS + `</style></head><body><div class="wrap"><div class="banner bad"><p>Could not read the ledger: {{.}}</p>
<p class="small">This is a failure to read, not an empty record. Reload to try again.</p></div></div></body></html>`))

// loadFailure is a read failure shown as one, never an empty record.
func loadFailure(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_ = failureTmpl.Execute(w, err.Error())
}
