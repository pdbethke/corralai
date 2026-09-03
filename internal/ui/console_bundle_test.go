// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// TestBuildManifest pins buildManifest's contract: Version/Entry pass
// through, one BundleAsset per file with an INDEPENDENTLY computed sha256,
// and a stable (sorted-by-path) asset order regardless of walk order.
func TestBuildManifest(t *testing.T) {
	testFS := fstest.MapFS{
		"index.html":       {Data: []byte("<html>hello</html>")},
		"replay-player.js": {Data: []byte("console.log('replay')")},
		"style.css":        {Data: []byte("body{color:red}")},
	}

	m, err := buildManifest(testFS, "v1.2.3")
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	if m.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", m.Version)
	}
	if m.Entry != "index.html" {
		t.Errorf("Entry = %q, want index.html", m.Entry)
	}
	if len(m.Assets) != 3 {
		t.Fatalf("Assets len = %d, want 3: %+v", len(m.Assets), m.Assets)
	}
	// Stable, sorted order.
	wantOrder := []string{"index.html", "replay-player.js", "style.css"}
	for i, want := range wantOrder {
		if m.Assets[i].Path != want {
			t.Errorf("Assets[%d].Path = %q, want %q (order not stable/sorted)", i, m.Assets[i].Path, want)
		}
	}
	// Independently computed hash per file.
	for path, f := range testFS {
		sum := sha256.Sum256(f.Data)
		want := hex.EncodeToString(sum[:])
		var got string
		for _, a := range m.Assets {
			if a.Path == path {
				got = a.SHA256
			}
		}
		if got == "" {
			t.Fatalf("no asset for %q", path)
		}
		if got != want {
			t.Errorf("asset %q sha256 = %q, want %q", path, got, want)
		}
	}
}

// TestBuildManifestSkipsDirectories: WalkDir visits directory entries too;
// buildManifest must emit assets only for files.
func TestBuildManifestSkipsDirectories(t *testing.T) {
	testFS := fstest.MapFS{
		"sub/nested.js": {Data: []byte("x")},
	}
	m, err := buildManifest(testFS, "v0")
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	if len(m.Assets) != 1 || m.Assets[0].Path != "sub/nested.js" {
		t.Fatalf("Assets = %+v, want exactly [sub/nested.js]", m.Assets)
	}
}

// TestConsoleManifestEndpoint proves GET /console/manifest.json serves the
// manifest built from the daemon's real embedded web/ FS, stamped with
// Deps.Version.
func TestConsoleManifestEndpoint(t *testing.T) {
	h := Handler(Deps{Version: "test-version", MemOwners: map[string]bool{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console/manifest.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var m BundleManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Version != "test-version" {
		t.Errorf("Version = %q, want test-version", m.Version)
	}
	if m.Entry != "index.html" {
		t.Errorf("Entry = %q, want index.html", m.Entry)
	}
	if len(m.Assets) == 0 {
		t.Fatal("Assets empty — expected the real embedded web/ files")
	}
}

// TestConsoleAssetEndpoint proves GET /console/asset/index.html returns the
// exact bytes the manifest's hash was computed over, and that path
// traversal is rejected.
func TestConsoleAssetEndpoint(t *testing.T) {
	h := Handler(Deps{MemOwners: map[string]bool{}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console/asset/index.html", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != string(want) {
		t.Error("served asset bytes don't match the embedded file")
	}

	for _, bad := range []string{
		"/console/asset/../main.go",
		"/console/asset/..%2f..%2fmain.go",
		"/console/asset//etc/passwd",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("path %q: status = 200, want traversal rejected", bad)
		}
	}
}

// TestConsoleManifestSigRoundTrips is the end-to-end proof: the committed
// dev signature (internal/ui/console.manifest.sig) verifies against
// ConsoleReleasePubKeyHex over the EXACT bytes GET /console/manifest.json
// serves when built with the same version the dev sig was signed for.
func TestConsoleManifestSigRoundTrips(t *testing.T) {
	// The signature file carries ONE ENTRY PER VERSION now, because the
	// manifest's version is inside the signed bytes and one signature covers
	// exactly one build. This test is about the DEV entry — the one an
	// unstamped `go run ./cmd/corral` needs — so it looks that entry up rather
	// than decoding the whole file, which it did while only one entry could
	// ever exist.
	raw, ok := ConsoleSigForVersion(devConsoleSignedVersion)
	if !ok || len(raw) == 0 {
		t.Fatalf("internal/ui/console.manifest.sig has no %q entry; run scripts/sign-console-bundle.sh %s", devConsoleSignedVersion, devConsoleSignedVersion)
	}
	sigBytes, err := hex.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("the %q signature is not valid hex: %v", devConsoleSignedVersion, err)
	}
	pubBytes, err := hex.DecodeString(ConsoleReleasePubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		t.Fatalf("ConsoleReleasePubKeyHex invalid: err=%v len=%d", err, len(pubBytes))
	}
	pub := ed25519.PublicKey(pubBytes)

	h := Handler(Deps{Version: devConsoleSignedVersion, MemOwners: map[string]bool{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console/manifest.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest.json status = %d", rec.Code)
	}
	served := rec.Body.Bytes()

	if !ed25519.Verify(pub, served, sigBytes) {
		t.Fatal("committed dev signature does NOT verify against the served manifest bytes — sign/verify round-trip broken")
	}

	// The sig endpoint must serve exactly the embedded bytes.
	sigRec := httptest.NewRecorder()
	h.ServeHTTP(sigRec, httptest.NewRequest(http.MethodGet, "/console/manifest.sig", nil))
	if sigRec.Code != http.StatusOK {
		t.Fatalf("manifest.sig status = %d", sigRec.Code)
	}
	// The daemon serves the entry for ITS OWN version, not the whole file. That
	// is the point of the per-version format: the manifest's version is inside
	// the signed bytes, so serving any other entry hands a client a signature
	// that cannot verify — which is precisely what every released brain did
	// while the file could hold only one.
	if sigRec.Body.String() != string(raw) {
		t.Errorf("GET /console/manifest.sig served %q, want the %q entry %q", sigRec.Body.String(), devConsoleSignedVersion, raw)
	}
}

// TestConsoleManifestSigMissingIs404 proves the fail-closed contract: a
// Server with no embedded signature bytes 404s /console/manifest.sig rather
// than fabricating one.
func TestConsoleManifestSigMissingIs404(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.consoleManifestSigHandler(rec, httptest.NewRequest(http.MethodGet, "/console/manifest.sig", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no signature is configured/embedded", rec.Code)
	}
}

// ONE FILE MUST CARRY MANY VERSIONS, because the manifest's Version is inside
// the signed bytes and one signature therefore covers exactly one build.
//
// A single-entry file forces a choice between a working development tree and a
// working release, and this repository made that choice silently, in favour of
// development, for its entire life: every released brain served a signature no
// thin client could verify.
func TestConsoleSigForVersionSelectsTheRightEntry(t *testing.T) {
	file := []byte("dev aaaa\nv0.8.4 bbbb\n# a comment\n\nv0.9.0 cccc\n")
	for _, tc := range []struct {
		version, want string
		found         bool
	}{
		{"dev", "aaaa", true},
		{"v0.8.4", "bbbb", true},
		{"v0.9.0", "cccc", true},
		{"v0.8.5", "", false},
		{"", "", false},
	} {
		got, ok := consoleSigForVersionIn(file, tc.version)
		if ok != tc.found || string(got) != tc.want {
			t.Errorf("version %q: got (%q, %v), want (%q, %v)", tc.version, got, ok, tc.want, tc.found)
		}
	}
}

// A LEGACY BARE SIGNATURE STAYS VALID. The format that shipped before was a
// single hex signature with no version column, produced only ever for "dev" —
// so that is what it means. Requiring every existing signature to be
// regenerated to adopt a new format would be a migration nobody would run.
func TestConsoleSigForVersionReadsTheLegacyBareFormat(t *testing.T) {
	bare := []byte("  deadbeef\n")
	got, ok := consoleSigForVersionIn(bare, devConsoleSignedVersion)
	if !ok || string(got) != "deadbeef" {
		t.Errorf("bare file for %q: got (%q, %v), want (\"deadbeef\", true)", devConsoleSignedVersion, got, ok)
	}
	if _, ok := consoleSigForVersionIn(bare, "v0.8.4"); ok {
		t.Error("a bare legacy signature must NOT be served for a release version — it was produced for \"dev\" and verifies against nothing else")
	}
	if _, ok := consoleSigForVersionIn([]byte("   \n"), devConsoleSignedVersion); ok {
		t.Error("an empty file must yield no signature; the daemon 404s rather than serving nothing as something")
	}
}

// THE COMMITTED FILE MUST STILL CARRY A dev ENTRY. Losing it would break every
// unstamped build — `go run ./cmd/corral` — while leaving CI green, because
// CI's own checks would happily verify whatever release entry remained.
func TestCommittedSignatureStillCoversDev(t *testing.T) {
	if _, ok := ConsoleSigForVersion(devConsoleSignedVersion); !ok {
		t.Fatalf("the committed signature file has no %q entry — an ordinary `go run ./cmd/corral` would serve a console no client can verify", devConsoleSignedVersion)
	}
}
