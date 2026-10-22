// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
)

// gzipMagic is the two bytes every gzip stream opens with. An entry is
// recognised by them rather than by its file name, because a browser fetches
// bytes and a CDN may serve a ".json.gz" already decompressed.
var gzipMagic = []byte{0x1f, 0x8b}

// ParseEntry parses one ledger entry from its bytes, gzipped or plain. name
// is the file the bytes came from; it becomes Entry.File and prefixes every
// error. Raw is set to the DECOMPRESSED bytes, because from corral-ledger-3
// the hash is over what the file says, not what this binary's struct would
// re-marshal (see EntryHash). The format must be one this reader knows: an
// entry in a format from the future is refused, not guessed at.
func ParseEntry(name string, raw []byte) (Entry, error) {
	if bytes.HasPrefix(raw, gzipMagic) {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return Entry{}, fmt.Errorf("ledger: %s: %w", name, err)
		}
		defer zr.Close()
		if raw, err = io.ReadAll(zr); err != nil {
			return Entry{}, fmt.Errorf("ledger: %s: %w", name, err)
		}
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, fmt.Errorf("ledger: %s: %w", name, err)
	}
	if !KnownFormat(e.Format) {
		return Entry{}, fmt.Errorf("ledger: %s: format %q, want %q", name, e.Format, FileFormat)
	}
	e.Raw, e.File = raw, name
	return e, nil
}
