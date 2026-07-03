// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// brainCall is the typed adapter the mirror helpers use: call a brain tool,
// get back a parsed map or an error. The agent's brain closure returns raw JSON
// strings; adapt it to this shape with makeBrainCall below.
type brainCall func(tool string, args map[string]any) (map[string]any, error)

// mirror tracks each repo mission's local working copy and the paths the bee wrote.
type mirror struct {
	root   string
	writes map[int64]map[string]bool
	base   map[int64]string
	isRepo map[int64]bool
	known  map[int64]bool // ensure() already ran for this mission
}

func newMirror(root string) *mirror {
	return &mirror{
		root:   root,
		writes: map[int64]map[string]bool{},
		base:   map[int64]string{},
		isRepo: map[int64]bool{},
		known:  map[int64]bool{},
	}
}

func (m *mirror) dir(missionID int64) string {
	return filepath.Join(m.root, "m"+itoa(missionID))
}

// ensure pulls the mission's snapshot once and lays it down under <root>/m<id>.
// For a non-repo mission (brain replies "not on a repo mission") it returns
// isRepo=false with no error — the caller falls back to the global workspace.
func (m *mirror) ensure(call brainCall, missionID int64) (string, bool, error) {
	if m.known[missionID] {
		if !m.isRepo[missionID] {
			// non-repo mission: never hand back a phantom dir that was never created.
			return "", false, nil
		}
		return m.dir(missionID), true, nil
	}
	m.known[missionID] = true
	res, err := call("repo_snapshot", map[string]any{})
	if err != nil {
		// "unknown tool" = this brain runs without the repo engine at all (the
		// demo default) — same meaning as a non-repo mission, not a warning to
		// print on every bee boot.
		if strings.Contains(err.Error(), "not on a repo mission") || strings.Contains(err.Error(), "unknown tool") {
			m.isRepo[missionID] = false
			return "", false, nil
		}
		return "", false, err
	}
	wd := m.dir(missionID)
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return "", false, err
	}
	data, err := base64.StdEncoding.DecodeString(asString(res["data_b64"]))
	if err != nil {
		return "", false, err
	}
	if err := untarGzInto(wd, data); err != nil {
		return "", false, err
	}
	m.base[missionID] = asString(res["base_rev"])
	m.isRepo[missionID] = true
	m.writes[missionID] = map[string]bool{}
	return wd, true, nil
}

// track records that the bee wrote path inside this mission's mirror.
func (m *mirror) track(missionID int64, path string) {
	if m.writes[missionID] == nil {
		m.writes[missionID] = map[string]bool{}
	}
	m.writes[missionID][filepath.ToSlash(path)] = true
}

// push sends exactly the tracked bee-written files back to the brain via repo_push.
// Build artifacts or any un-tracked paths are never sent.
func (m *mirror) push(call brainCall, missionID int64) ([]string, error) {
	tracked := m.writes[missionID]
	if len(tracked) == 0 {
		return nil, nil
	}
	files := make([]map[string]any, 0, len(tracked))
	for p := range tracked {
		b, err := os.ReadFile(filepath.Join(m.dir(missionID), p)) // #nosec G304 -- path confined under bee's own mirror workspace; p is a tracked path set by m.track() from server-controlled write operations only
		if err != nil {
			continue // tracked file vanished — skip
		}
		files = append(files, map[string]any{"path": p, "content": string(b)})
	}
	if len(files) == 0 {
		// every tracked file vanished between track() and push() — nothing to send.
		m.writes[missionID] = map[string]bool{}
		return nil, nil
	}
	res, err := call("repo_push", map[string]any{
		"files":    files,
		"base_rev": m.base[missionID],
	})
	if err != nil {
		return nil, err
	}
	m.writes[missionID] = map[string]bool{} // clear after a successful push
	// Fix 4: refresh base so same-worker multi-phase missions don't perpetually
	// report stale==true after the first push. The brain echoes HEAD as base_rev.
	if rev := asString(res["base_rev"]); rev != "" {
		m.base[missionID] = rev
	}
	var applied []string
	if a, ok := res["applied"].([]any); ok {
		for _, x := range a {
			applied = append(applied, asString(x))
		}
	}
	return applied, nil
}

// untarGzInto extracts a gzipped tar archive into dir, confining all paths to dir
// (rejects absolute paths and directory traversal). The bee NEVER receives a .git
// directory — the snapshot layer strips it — so no .git filtering is needed here.
func untarGzInto(dir string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	const mirrorCap = 256 << 20 // bound total extracted bytes (decompression-bomb guard)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// filepath.Clean("/" + name) collapses any "../" and ensures the path starts
		// with "/" — filepath.Join then roots it under dir, bounding all writes.
		clean := filepath.Clean("/" + h.Name)
		full := filepath.Join(dir, clean)
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(full, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- path confined under bee's own mirror workspace via filepath.Clean("/"+h.Name) bounding all writes to dir; not attacker input
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(tr, mirrorCap-total+1))
		_ = f.Close()
		if err != nil {
			return err
		}
		total += n
		if total > mirrorCap {
			return fmt.Errorf("mirror archive exceeds %d bytes (decompression-bomb guard)", int64(mirrorCap))
		}
	}
	return nil
}

// writeSucceeded reports whether a dispatch result JSON has ok==true. Parsing the
// JSON (rather than substring-matching `"ok":true`) keeps write-tracking robust to
// formatting — `{"ok": true}` with a space is still valid and must count as success,
// otherwise a brain-side formatting change would silently stop tracking writes and
// push() would ship nothing (silent data loss).
func writeSucceeded(result string) bool {
	var r map[string]any
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		return false
	}
	ok, _ := r["ok"].(bool)
	return ok
}

// itoa converts an int64 to a decimal string.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// asString does a safe type-assert from any to string.
func asString(v any) string { s, _ := v.(string); return s }
