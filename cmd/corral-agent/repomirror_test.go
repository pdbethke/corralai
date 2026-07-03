// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeBrain returns a snapshot for mission 7 and records pushes.
func fakeBrain(t *testing.T) (brainCall, *[]pushedFile) {
	var pushed []pushedFile
	// build a tiny tar.gz with one file
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("package calc\n")
	tw.WriteHeader(&tar.Header{Name: "calc.go", Mode: 0o644, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	call := func(tool string, args map[string]any) (map[string]any, error) {
		switch tool {
		case "repo_snapshot":
			return map[string]any{"data_b64": b64, "manifest": map[string]any{"calc.go": "x"}, "base_rev": "abc123"}, nil
		case "repo_push":
			for _, f := range args["files"].([]map[string]any) {
				pushed = append(pushed, pushedFile{f["path"].(string), f["content"].(string)})
			}
			return map[string]any{"applied": []any{}, "stale": false}, nil
		}
		return map[string]any{}, nil
	}
	return call, &pushed
}

type pushedFile struct{ path, content string }

// errNotRepoMission is the sentinel the TestEnsureNonRepoMission fake brain returns.
// ensure() recognises it (and any brain-level "not on a repo mission" string) as
// "plain mission" → isRepo=false, no error.
var errNotRepoMission = errors.New("not on a repo mission")

func TestMirrorPullTrackPush(t *testing.T) {
	root := t.TempDir()
	call, pushed := fakeBrain(t)
	m := newMirror(root)

	wd, isRepo, err := m.ensure(call, 7)
	if err != nil || !isRepo {
		t.Fatalf("ensure: wd=%s isRepo=%v err=%v", wd, isRepo, err)
	}
	if wd != filepath.Join(root, "m7") {
		t.Fatalf("workdir = %s", wd)
	}
	if _, err := os.Stat(filepath.Join(wd, "calc.go")); err != nil {
		t.Fatalf("snapshot not laid down: %v", err)
	}
	// SECURITY: the mirror must never contain a .git directory — the credential
	// boundary that prevents bees from accessing repo history or tokens.
	if _, err := os.Stat(filepath.Join(wd, ".git")); !os.IsNotExist(err) {
		t.Fatal("mirror must contain no .git (credential boundary)")
	}

	// simulate a bee edit + a build artifact
	os.WriteFile(filepath.Join(wd, "calc.go"), []byte("package calc\n// edited\n"), 0o644)
	m.track(7, "calc.go")
	os.WriteFile(filepath.Join(wd, "calc"), []byte("ELF-binary"), 0o755) // build artifact, NOT tracked

	if _, err := m.push(call, 7); err != nil {
		t.Fatal(err)
	}
	if len(*pushed) != 1 || (*pushed)[0].path != "calc.go" {
		t.Fatalf("expected only calc.go pushed, got %v", *pushed)
	}
	if (*pushed)[0].content != "package calc\n// edited\n" {
		t.Fatalf("pushed stale content: %q", (*pushed)[0].content)
	}
}

func TestWriteSucceeded(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"ok":true}`, true},
		{`{"ok": true}`, true}, // a space is still valid JSON — must count
		{`{"ok":true,"path":"x","bytes":3}`, true},
		{`{"ok":false}`, false},
		{`{"error":"path required"}`, false},
		{`not json`, false},
		{``, false},
		{`{"ok":"true"}`, false}, // string, not bool
	}
	for _, c := range cases {
		if got := writeSucceeded(c.in); got != c.want {
			t.Errorf("writeSucceeded(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestUntarGzIntoTraversal locks the path-confinement defense: a tar entry that
// tries to escape via ../ must land INSIDE the target dir, never in its parent.
func TestUntarGzIntoTraversal(t *testing.T) {
	parent := t.TempDir()
	dst := filepath.Join(parent, "jail")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("escaped!\n")
	tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0o644, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()

	if err := untarGzInto(dst, buf.Bytes()); err != nil {
		t.Fatalf("untarGzInto: %v", err)
	}
	// The malicious entry must NOT have escaped into parent (or anywhere above dst).
	if _, err := os.Stat(filepath.Join(parent, "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("traversal escaped: file written outside the jail")
	}
	// It is confined under dst instead (the cleaned path roots at dst/escape.txt).
	if _, err := os.Stat(filepath.Join(dst, "escape.txt")); err != nil {
		t.Fatalf("confined file not found inside jail: %v", err)
	}
}

func TestEnsureNonRepoMission(t *testing.T) {
	root := t.TempDir()
	call := func(tool string, args map[string]any) (map[string]any, error) {
		if tool == "repo_snapshot" {
			return nil, errNotRepoMission
		}
		return map[string]any{}, nil
	}
	m := newMirror(root)
	wd, isRepo, err := m.ensure(call, 3)
	if err != nil || isRepo || wd != "" {
		t.Fatalf("non-repo mission should be isRepo=false, no error, no phantom path; got wd=%q isRepo=%v err=%v", wd, isRepo, err)
	}
	// Cached call must be consistent: still no phantom dir on the second pass.
	wd, isRepo, err = m.ensure(call, 3)
	if err != nil || isRepo || wd != "" {
		t.Fatalf("cached non-repo mission inconsistent; got wd=%q isRepo=%v err=%v", wd, isRepo, err)
	}
}
