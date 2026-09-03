// SPDX-License-Identifier: Elastic-2.0

// Package consolebundle is the console bundle's signed-manifest vocabulary,
// and it is a LEAF ON PURPOSE: standard library only, no corralai imports,
// nothing that reaches the engine.
//
// It exists because of what its absence cost. These types lived in
// internal/ui, and internal/console — the 747-line reverse proxy the
// observer, admin and desktop clients share — imported internal/ui to reach
// them. internal/ui imports 21 internal packages, so that single edge linked
// DuckDB, SQLite and twelve tree-sitter grammars into every client:
// corral-observe was 115 MB and dynamically bound to libstdc++, which is why
// deploy/observe/Dockerfile could not build (CGO_ENABLED=0 against a C++
// dependency) and could not have run if it had (distroless/static ships no
// libstdc++).
//
// The README states the intended architecture — "corral holds the state and
// authority; everything else connects over MCP/HTTP", the brain being "the one
// binary that cares about its platform". That was true when written. A client
// verifies a manifest; it does not need the engine that produced one, and the
// two symbols it actually used (BundleManifest, ReleasePubKeyHex) never did.
//
// So: keep this package free of corralai imports.
// TestDocsFleetTableCGOColumnIsTrue (cmd/corral/clientweight_test.go) enforces
// the consequence rather than the rule — it builds each binary the README's
// fleet table marks CGO "no" and fails if one needs cgo, so the table is a
// claim a run has to keep passing.
//
// That name was wrong here for a day: this comment cited
// TestClientBinariesAreCGOFree, which does not exist and never did. A comment
// asserting a gate that is not there is worse than no comment — it is the
// "comments claiming more than the code" failure this repository keeps paying
// for, committed in the same change that wrote the rule down.
package consolebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sort"
)

// BundleManifest is the versioned, signed description of the console SPA
// bundle the daemon serves for thin clients. It is the trust anchor a thin
// client checks (via console.manifest.sig, verified against a pinned or
// configured release public key) before rendering the served HTML/JS as this
// brain's console.
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

// BuildManifest walks sub (normally fs.Sub(webFS, "web")) and returns the
// manifest for version: one BundleAsset per file, hex sha256, sorted by
// path. The daemon computes this once per Handler construction and caches it
// — it is never recomputed per-request.
//
// The sort is what makes the manifest signable: the embed FS's walk order is
// not part of the contract, and an unsorted manifest would produce different
// canonical bytes — and so a different signature — for identical assets.
func BuildManifest(sub fs.FS, version string) (BundleManifest, error) {
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

// DevSignedVersion is the version string the COMMITTED dev signature
// (internal/ui/console.manifest.sig) was produced for. It must match whatever
// version the daemon serves for the dev round-trip verification to pass —
// "dev" mirrors cmd/corral/main.go's unbuilt-default `var version = "dev"`, so
// an ordinary `go run ./cmd/corral` (no -ldflags) verifies out of the box.
const DevSignedVersion = "dev"

// ReleasePubKeyHex is the DEV Ed25519 public key (hex-encoded) that verifies
// the committed console.manifest.sig. This is a DEV/TEST key only — its seed
// lives in scripts/dev-console-signing-key.hex, openly committed because it
// signs nothing that matters beyond local dev and CI. A REAL release re-signs
// with $CORRALAI_RELEASE_KEY (scripts/sign-console-bundle.sh) and ships its
// OWN public key to clients out-of-band; this constant is a convenient,
// overridable dev default, never a production trust anchor.
const ReleasePubKeyHex = "584415516982331723bd400873056aad4b367a30b9cb087adabfe4de0f16e938"
