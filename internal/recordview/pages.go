// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"
)

// IndexRows is how many audits and reviews the index shows before linking
// the full list; ListRows is the page size of the full lists. The whole
// chain on one page was 646K characters in corral ui on 2026-10-18.
const (
	IndexRows = 25
	ListRows  = 100
)

// ErrNotFound is a path this site has no page for.
var ErrNotFound = errors.New("recordview: no such page")

// Pages holds the parsed templates. Parse once, render many times.
type Pages struct {
	index, entry, scan, list, chain, verdicts *template.Template
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"short":    shortHash,
		"hasPre":   func(s string) bool { return strings.TrimSpace(s) != "" },
		"date":     func(t time.Time) string { return t.UTC().Format("2006-01-02") },
		"stamp":    func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
		"join":     func(s []string) string { return strings.Join(s, ", ") },
		"lower":    strings.ToLower,
		"ctl":      func(f FindingView, sha string) controlsData { return controlsData{f, sha} },
		"entryURL": func(short string) string { return "entry/" + short + ".html" },
		"thousands": func(n int64) string {
			if n >= 1_000_000 {
				return fmt.Sprintf("%.1fM", float64(n)/1e6)
			}
			if n >= 1_000 {
				return fmt.Sprintf("%.1fk", float64(n)/1e3)
			}
			return fmt.Sprint(n)
		},
	}
}

// NewPages parses every template. An error here is a bug in this package.
func NewPages() (*Pages, error) {
	p := &Pages{}
	for _, t := range []struct {
		dst  **template.Template
		name string
		src  string
	}{
		{&p.index, "index", indexTmpl},
		{&p.entry, "entry", entryTmpl},
		{&p.scan, "scan", scanTmpl},
		{&p.list, "list", listTmpl},
		{&p.chain, "chain", chainTmpl},
		{&p.verdicts, "verdicts", verdictsTmpl},
	} {
		parsed, err := template.New(t.name).Funcs(funcs()).Parse(t.src)
		if err != nil {
			return nil, fmt.Errorf("recordview: parsing %s: %w", t.name, err)
		}
		*t.dst = parsed
	}
	return p, nil
}

// listPage is one page of a full list.
type listPage struct {
	Site
	What       string // "audits", "reviews"
	N, Of      int
	Prev, Next string
	Scans      []ScanView
	Reviews    []EntryView
}

// chainPage is one page of the chain.
type chainPage struct {
	Site
	N, Of      int
	Prev, Next string
	Rows       []ChainView
}

func pages(n, size int) int {
	if n == 0 {
		return 1
	}
	return (n + size - 1) / size
}

func window[T any](xs []T, page, size int) []T {
	lo := (page - 1) * size
	hi := min(lo+size, len(xs))
	if lo >= len(xs) {
		return nil
	}
	return xs[lo:hi]
}

func neighbours(prefix string, n, of int) (prev, next string) {
	if n > 1 {
		prev = fmt.Sprintf("%s-%d.html", prefix, n-1)
	}
	if n < of {
		next = fmt.Sprintf("%s-%d.html", prefix, n+1)
	}
	return prev, next
}

// VerifiedEntries is how many entries the chain check actually saw.
func (s Site) VerifiedEntries() int { return s.LedgerEntries - s.Unverified }

// NewestScans and NewestReviews are what the index shows.
func (s Site) NewestScans() []ScanView    { return window(s.Scans, 1, IndexRows) }
func (s Site) NewestReviews() []EntryView { return window(s.Reviews, 1, IndexRows) }

// Paths is every page this site has, in a stable order: the index, the
// list and chain pages, then one page per review and per audit.
func (s Site) Paths() []string {
	out := []string{"index.html", "verdicts.html"}
	for _, l := range []struct {
		prefix string
		n      int
		size   int
	}{{"audits", len(s.Scans), ListRows}, {"reviews", len(s.Reviews), ListRows}, {"chain", len(s.Chain), ListRows}} {
		for i := 1; i <= pages(l.n, l.size); i++ {
			out = append(out, fmt.Sprintf("%s-%d.html", l.prefix, i))
		}
	}
	for _, r := range s.Reviews {
		out = append(out, "entry/"+r.Short+".html")
	}
	for _, sc := range s.Scans {
		out = append(out, "entry/"+sc.Short+".html")
	}
	return out
}

// Render writes the page at path. It is the only way any page is produced,
// static or live, which is what keeps the two identical.
func (p *Pages) Render(w io.Writer, s Site, path string) error {
	switch {
	case path == "index.html":
		return p.index.Execute(w, s)
	case path == "verdicts.html":
		return p.verdicts.Execute(w, s)
	case strings.HasPrefix(path, "entry/") && strings.HasSuffix(path, ".html"):
		short := strings.TrimSuffix(strings.TrimPrefix(path, "entry/"), ".html")
		if !isHex(short) {
			return ErrNotFound
		}
		for _, r := range s.Reviews {
			if r.Short == short {
				return p.entry.Execute(w, struct {
					EntryView
					Site Site
				}{r, s})
			}
		}
		for _, sc := range s.Scans {
			if sc.Short == short {
				return p.scan.Execute(w, struct {
					ScanView
					Site Site
				}{sc, s})
			}
		}
		return ErrNotFound
	}
	prefix, n, ok := listPath(path)
	if !ok {
		return ErrNotFound
	}
	switch prefix {
	case "audits", "reviews":
		total := len(s.Scans)
		if prefix == "reviews" {
			total = len(s.Reviews)
		}
		of := pages(total, ListRows)
		if n > of {
			return ErrNotFound
		}
		lp := listPage{Site: s, What: prefix, N: n, Of: of}
		lp.Prev, lp.Next = neighbours(prefix, n, of)
		if prefix == "audits" {
			lp.Scans = window(s.Scans, n, ListRows)
		} else {
			lp.Reviews = window(s.Reviews, n, ListRows)
		}
		return p.list.Execute(w, lp)
	case "chain":
		of := pages(len(s.Chain), ListRows)
		if n > of {
			return ErrNotFound
		}
		cp := chainPage{Site: s, N: n, Of: of, Rows: window(s.Chain, n, ListRows)}
		cp.Prev, cp.Next = neighbours("chain", n, of)
		return p.chain.Execute(w, cp)
	}
	return ErrNotFound
}

// listPath parses "<prefix>-<n>.html" for the three list kinds, n >= 1.
func listPath(path string) (string, int, bool) {
	base, ok := strings.CutSuffix(path, ".html")
	if !ok {
		return "", 0, false
	}
	prefix, num, ok := strings.Cut(base, "-")
	if !ok || (prefix != "audits" && prefix != "reviews" && prefix != "chain") {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || strconv.Itoa(n) != num {
		return "", 0, false
	}
	return prefix, n, true
}

// controlsData is what the "controls" template needs: the finding, and the
// short commit the scriptless note names.
type controlsData struct {
	F   FindingView
	SHA string
}
