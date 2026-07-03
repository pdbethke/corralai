// SPDX-License-Identifier: Elastic-2.0

package reference

import (
	"os"
	"strings"

	"github.com/pdbethke/corralai/internal/embed"
)

// Embedder is the shared embed client; kept as an alias so existing reference
// callers/tests are unchanged after the extract to internal/embed.
type Embedder = embed.Client

func NewEmbedder() *Embedder { return embed.New() }

func NewEmbedderFor(url, model, key string) *Embedder { return embed.NewFor(url, model, key) }

// chunk splits text into ~size-rune windows that overlap by overlap runes (so a
// fact spanning a boundary still lands whole in some chunk).
func chunk(text string, size, overlap int) []string {
	r := []rune(strings.TrimSpace(text))
	if len(r) == 0 {
		return nil
	}
	if size <= 0 {
		size = 1200
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 6
	}
	var out []string
	for start := 0; start < len(r); start += size - overlap {
		end := start + size
		if end > len(r) {
			end = len(r)
		}
		if s := strings.TrimSpace(string(r[start:end])); s != "" {
			out = append(out, s)
		}
		if end == len(r) {
			break
		}
	}
	return out
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
