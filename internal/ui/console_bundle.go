// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
)

// BundleManifest is the versioned, signed description of the console SPA
// bundle the daemon serves for thin clients (Task 1 of the daemon/client
// architecture refactor — see docs/superpowers/specs/2026-07-10-daemon-
// client-architecture-design.md). It is the trust anchor a thin client
// checks (via console.manifest.sig, verified against a pinned/configured
// release public key) before rendering the served HTML/JS as this brain's
// console.
type BundleManifest struct {
	// Version is the daemon's own build version (cmd/corral's `version`
	// var, normally set via -ldflags "-X main.version=..."). It is part of
	// the signed bytes, so a signature only verifies against the exact
	// release it was produced for.
	Version string `json:"version"`
	// Entry is always "index.html" — the SPA's boot document.
	Entry string `json:"entry"`
	// Assets is every file in the bundle, sorted by Path for a stable,
	// deterministic manifest regardless of the embed FS's own walk order.
	Assets []BundleAsset `json:"assets"`
}

// BundleAsset is one file within the console bundle: its path relative to
// the web/ root and a hex sha256 of its content, so a client can verify
// each asset it downloads against the signed manifest before using it.
type BundleAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// buildManifest walks sub (normally fs.Sub(webFS, "web")) and returns the
// manifest for version: one BundleAsset per file, hex sha256, sorted by
// path. Computed once per Handler construction and cached — never
// recomputed per-request.
func buildManifest(sub fs.FS, version string) (BundleManifest, error) {
	var assets []BundleAsset
	err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		assets = append(assets, BundleAsset{Path: p, SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return BundleManifest{}, err
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Path < assets[j].Path })
	return BundleManifest{Version: version, Entry: "index.html", Assets: assets}, nil
}

// CanonicalManifestBytes returns the exact JSON bytes buildManifest's result
// serializes to for version — the SAME bytes GET /console/manifest.json
// serves at runtime (Handler builds+caches this once via the identical
// buildManifest+json.Marshal path) and the same bytes
// scripts/sign-console-bundle.sh (via cmd/sign-console-bundle) signs.
// Exported specifically so the signing tool never reimplements the
// walk/marshal and risks drifting from what the daemon actually serves.
//
// json.Marshal is deterministic here: every BundleManifest field is a
// plain string or a []BundleAsset in the fixed, sorted order buildManifest
// already established — no map, so no key-ordering ambiguity (mirrors
// certify.CanonicalStatement's reasoning for the build-attestation
// statement).
func CanonicalManifestBytes(version string) ([]byte, error) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	m, err := buildManifest(sub, version)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// devConsoleSignedVersion is the version string the COMMITTED dev signature
// (console.manifest.sig, below) was produced for. It must match whatever
// Deps.Version the daemon serves for the dev round-trip test/verification
// to pass — "dev" mirrors cmd/corral/main.go's unbuilt-default `var version
// = "dev"`, so an ordinary `go run ./cmd/corral` (no -ldflags) verifies
// out of the box.
const devConsoleSignedVersion = "dev"

// ConsoleReleasePubKeyHex is the DEV Ed25519 public key (hex-encoded) that
// verifies the committed console.manifest.sig below. This is a DEV/TEST
// key only — its seed lives in scripts/dev-console-signing-key.hex, openly
// committed because it signs nothing that matters beyond local dev/CI. A
// REAL release re-signs with $CORRALAI_RELEASE_KEY (scripts/sign-console-
// bundle.sh) and ships its OWN public key to clients out-of-band (Task 2);
// this constant is a convenient, overridable dev default, never a
// production trust anchor.
const ConsoleReleasePubKeyHex = "584415516982331723bd400873056aad4b367a30b9cb087adabfe4de0f16e938"

// consoleManifestSigRaw is the detached Ed25519 signature (hex text) over
// CanonicalManifestBytes(devConsoleSignedVersion), produced by
// scripts/sign-console-bundle.sh. Embedded so the daemon can serve it
// without any signing capability of its own — the daemon NEVER mints this
// signature at runtime (see the package doc's trust model). If this file
// is empty/missing at build time, `go:embed` still compiles (embedding an
// empty file is fine); the handler then serves 404 rather than fabricate a
// signature.
//
//go:embed console.manifest.sig
var consoleManifestSigRaw []byte

// consoleManifestSig is the trimmed form actually compared/served — trims
// the trailing newline a text editor or `echo` (vs sign-console-bundle.sh's
// `printf`) might otherwise leave in the committed file.
var consoleManifestSig = bytes.TrimSpace(consoleManifestSigRaw)

// consoleManifestHandler serves the cached manifest JSON built once at
// Handler construction (s.consoleManifestJSON) — never recomputed
// per-request. A nil/empty cache (buildManifest failed at startup, logged
// there) serves a 500 rather than silently returning nothing a client
// could mistake for "no assets".
func (s *Server) consoleManifestHandler(w http.ResponseWriter, r *http.Request) {
	if len(s.consoleManifestJSON) == 0 {
		http.Error(w, "console bundle manifest unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.consoleManifestJSON)
}

// consoleManifestSigHandler serves the embedded detached signature exactly
// as committed. Fail-closed: an empty/absent embedded signature (a build
// that skipped scripts/sign-console-bundle.sh) 404s — the daemon NEVER
// fabricates or lazily mints a signature here. A client that requires a
// signature must then refuse the (now provably unsigned) manifest itself.
func (s *Server) consoleManifestSigHandler(w http.ResponseWriter, r *http.Request) {
	if len(s.consoleSig) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(s.consoleSig)
}

// consoleAsset serves one file out of s.consoleSub (the SAME fs.Sub(webFS,
// "web") tree "/" serves from) by the {path...} wildcard. Rejects any
// request path containing "..", an absolute path, or one that
// path.Clean-normalizes to escape the tree — belt-and-suspenders on top of
// http.ServeMux's own path cleaning/redirect behavior, since a client
// should never be able to pull an arbitrary file off the daemon's
// filesystem through this endpoint.
func (s *Server) consoleAsset(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if p == "" || strings.Contains(p, "..") || path.IsAbs(p) {
		http.Error(w, "invalid asset path", http.StatusBadRequest)
		return
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		http.Error(w, "invalid asset path", http.StatusBadRequest)
		return
	}
	if s.consoleSub == nil {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.consoleSub, clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A recognized extension gets its real Content-Type; anything else
	// falls back to a passive octet-stream rather than letting the browser
	// sniff it — nosniff on top closes the same stored-content class of
	// XSS lookbookImage guards against (see its doc comment).
	ct := mime.TypeByExtension(path.Ext(clean))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data) // #nosec G705 -- data is a file read from consoleSub (the same embedded web/ tree "/" already serves); clean was validated above to reject "..", absolute paths, and any path.Clean escape, and nosniff+explicit Content-Type close the sniff-based XSS class this rule flags
}
