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
	if len(consoleManifestSig) == 0 {
		t.Fatal("consoleManifestSig is empty — internal/ui/console.manifest.sig missing/empty; run scripts/sign-console-bundle.sh")
	}
	sigBytes, err := hex.DecodeString(string(consoleManifestSig))
	if err != nil {
		t.Fatalf("consoleManifestSig is not valid hex: %v", err)
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
	if sigRec.Body.String() != string(consoleManifestSig) {
		t.Error("GET /console/manifest.sig did not serve the exact embedded signature bytes")
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
