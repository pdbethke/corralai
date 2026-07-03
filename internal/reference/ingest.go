// SPDX-License-Identifier: Elastic-2.0

package reference

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/pdbethke/corralai/internal/pdftext"
)

// Ingest chunks text, embeds the chunks via the configured remote endpoint, and
// stores them under source (idempotently). Returns the chunk count.
func Ingest(s *Store, e *Embedder, source, kind, text string) (int, error) {
	if e == nil {
		return 0, fmt.Errorf("reference engine disabled: no embeddings endpoint (set CORRALAI_EMBED_URL)")
	}
	chunks := chunk(text, 1200, 200)
	if len(chunks) == 0 {
		return 0, nil
	}
	vecs, err := e.Embed(chunks)
	if err != nil {
		return 0, err
	}
	if len(vecs) != len(chunks) {
		return 0, fmt.Errorf("embedding count mismatch: %d chunks, %d vectors", len(chunks), len(vecs))
	}
	cs := make([]Chunk, len(chunks))
	for i := range chunks {
		cs[i] = Chunk{Seq: i, Text: chunks[i], Embedding: vecs[i]}
	}
	if err := s.Replace(source, kind, cs); err != nil {
		return 0, err
	}
	return len(cs), nil
}

// FetchText GETs a URL (using the supplied client — pass an SSRF-guarded one) and
// returns its plain-ish text (script/style dropped, tags stripped, entities
// unescaped, whitespace collapsed).
func FetchText(httpc *http.Client, url string) (string, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %d", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if pdftext.IsPDF(body) {
		return pdftext.Extract(body)
	}
	return htmlToText(string(body)), nil
}

var (
	reScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reStyle  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reWS     = regexp.MustCompile(`\s+`)
)

func htmlToText(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	s = reStyle.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
