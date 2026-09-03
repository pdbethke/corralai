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
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
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

// DevPubKeyHex is the DEV Ed25519 public key, and its PRIVATE HALF IS
// PUBLISHED: the seed sits in scripts/dev-console-signing-key.hex, committed to
// a public repository. Anyone can sign a console bundle that verifies against
// it.
//
// That is fine for what it is — a key for local development and CI, signing
// nothing that matters — and it was catastrophic as what it had become: the
// PINNED, ONLY, NON-OVERRIDABLE trust anchor every thin client used. A hostile
// or compromised daemon could serve HTML and JavaScript signed with a key
// printed in the repository, and corral-observe or corral-admin would verify
// it, cache it, and render it same-origin with the console session — carrying
// the operator's injected bearer to the brain. Meanwhile the verification code
// stated that "a daemon (or a MITM) cannot supply its own key and have a client
// trust it."
//
// So it is no longer a default. It is accepted ONLY when the operator asks for
// it by name, and TrustAnchor says so out loud.
const DevPubKeyHex = "584415516982331723bd400873056aad4b367a30b9cb087adabfe4de0f16e938"

// Environment variables that select the trust anchor. These are read rather
// than baked in because the primary install path is `go install` from source,
// which sets no -ldflags: a build-stamped key cannot reach the binary most
// operators actually run, so the anchor has to be configuration.
const (
	// TrustAnchorEnv names the release public key, hex-encoded, that this
	// client will accept. This is the production setting.
	TrustAnchorEnv = "CORRALAI_CONSOLE_PUBKEY"
	// DevAnchorEnv opts in to the published DEV key. It exists so local
	// development and CI keep working with one variable, and it is the only
	// way to reach a key whose private half anyone can read.
	DevAnchorEnv = "CORRALAI_CONSOLE_DEV"
)

// TrustAnchor resolves the Ed25519 public key a client will verify the console
// manifest against, and reports where it came from.
//
// THERE IS NO DEFAULT, deliberately. A default anchor whose private key is
// published is worse than no anchor at all, because it converts "unverified"
// into "verified" — the exact inversion this project exists to refuse
// elsewhere. Refusing is honest and actionable; trusting a published key is
// neither.
//
// Order: the operator's configured key, then the published dev key if and only
// if the operator opted in.
func TrustAnchor() (pubHex, source string, err error) {
	if v := strings.TrimSpace(os.Getenv(TrustAnchorEnv)); v != "" {
		if _, decErr := hex.DecodeString(v); decErr != nil || len(v) != 64 {
			return "", "", fmt.Errorf("%s is not a 64-character hex Ed25519 public key (got %d chars): %v", TrustAnchorEnv, len(v), decErr)
		}
		// THE PUBLISHED DEV KEY IS NOT A RELEASE KEY, however it arrives.
		// Passing it through this variable made it the production anchor
		// with no warning at all, because the warning keys on the DEV
		// source. It is the same key with the same public seed; the only
		// honest way to accept it is to say so, which is what the DEV opt-in
		// exists for.
		if strings.EqualFold(v, DevPubKeyHex) {
			return "", "", fmt.Errorf("%s is set to the PUBLISHED development key, whose private half is committed to corralai's public repository — it is not a release key. If you mean to use it for local work, set %s=1 instead, so the client logs that it is trusting a key anyone can sign with", TrustAnchorEnv, DevAnchorEnv)
		}
		return v, TrustAnchorEnv, nil
	}
	if truthy(os.Getenv(DevAnchorEnv)) {
		return DevPubKeyHex, DevAnchorEnv, nil
	}
	return "", "", fmt.Errorf(
		"no console trust anchor is configured, so nothing can vouch for the HTML and JavaScript this client would render.\n"+
			"  For a real deployment: set %s to your release public key (hex), obtained out-of-band from whoever signs your console bundle.\n"+
			"  For local development against a dev brain: set %s=1, which accepts the key in scripts/dev-console-signing-key.hex — whose PRIVATE half is committed to this public repository, so it proves only that the bundle was signed by someone who can read GitHub.",
		TrustAnchorEnv, DevAnchorEnv)
}

// truthy accepts the spellings an operator actually types.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
