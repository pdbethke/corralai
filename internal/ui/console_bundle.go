// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/pdbethke/corralai/internal/consolebundle"
)

// BundleManifest and BundleAsset are ALIASES for the leaf package's types,
// not copies: internal/console (the clients' shared reverse proxy) reads the
// same manifest this package serves, and it must be able to do so without
// importing internal/ui — which would relink DuckDB, SQLite and twelve
// tree-sitter grammars into every thin client. See internal/consolebundle's
// package doc for what that edge cost. Aliases (not definitions) keep the
// JSON encoding byte-identical, so the committed signature stays valid.
type BundleManifest = consolebundle.BundleManifest

// BundleAsset is one file within the console bundle. See BundleManifest.
type BundleAsset = consolebundle.BundleAsset

// buildManifest is retained as a thin call-through so this package's own
// call sites and tests read unchanged.
func buildManifest(sub fs.FS, version string) (BundleManifest, error) {
	return consolebundle.BuildManifest(sub, version)
}

// devConsoleSignedVersion is the version the COMMITTED dev signature was
// produced for. See consolebundle.DevSignedVersion.
const devConsoleSignedVersion = consolebundle.DevSignedVersion

// ConsoleReleasePubKeyHex is the DEV Ed25519 public key that verifies the
// COMMITTED console.manifest.sig. It is used here only to check that the
// signature in this repository matches the bundle in this repository — a
// self-consistency check, not a trust decision.
//
// Clients do NOT get their anchor from here: see consolebundle.TrustAnchor,
// which refuses to default to this key because its private half is committed.
const ConsoleReleasePubKeyHex = consolebundle.DevPubKeyHex

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
