// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFetchBundleVerifiesAndCachesAssets(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	cache := t.TempDir()

	dir, m, err := fetchBundle(srv.URL, "tok", cache, false)
	if err != nil {
		t.Fatalf("fetchBundle: %v", err)
	}
	if m.Version != "v1" {
		t.Fatalf("Version = %q, want v1", m.Version)
	}
	if len(m.Assets) != 2 {
		t.Fatalf("Assets = %+v, want 2", m.Assets)
	}
	for _, a := range m.Assets {
		got, err := os.ReadFile(filepath.Join(dir, a.Path))
		if err != nil {
			t.Fatalf("asset %q not cached: %v", a.Path, err)
		}
		if sha256Hex(got) != a.SHA256 {
			t.Fatalf("asset %q on-disk sha256 mismatch", a.Path)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("bundle dir perm = %o, want 0700", perm)
		}
	}
}

func TestFetchBundleIdempotentNoRedownload(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	cache := t.TempDir()

	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err != nil {
		t.Fatalf("first fetchBundle: %v", err)
	}
	before := d.assetHits

	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err != nil {
		t.Fatalf("second fetchBundle: %v", err)
	}
	if d.assetHits != before {
		t.Fatalf("second call re-downloaded assets: hits %d -> %d, want no change", before, d.assetHits)
	}
}

func TestFetchBundleTamperedSignatureRefused(t *testing.T) {
	for _, mode := range []string{"tampered", "wrongkey"} {
		t.Run(mode, func(t *testing.T) {
			d := newFakeDaemon(t)
			d.setSigMode(mode)
			srv := d.server(t)
			cache := t.TempDir()

			dir, _, err := fetchBundle(srv.URL, "tok", cache, false)
			if err == nil {
				t.Fatalf("fetchBundle succeeded with a %s signature, want refusal", mode)
			}
			if dir != "" {
				t.Errorf("dir = %q, want empty on refusal", dir)
			}
			if entries, _ := os.ReadDir(filepath.Join(cache, "console")); len(entries) > 0 {
				for _, e := range entries {
					if !e.IsDir() || filepath.Ext(e.Name()) == ".hwm" {
						continue
					}
					t.Errorf("cache dir %q exists after signature refusal — nothing should be cached", e.Name())
				}
			}
		})
	}
}

func TestFetchBundleTamperedManifestRefused(t *testing.T) {
	d := newFakeDaemon(t)
	d.setManifestTamper(true)
	srv := d.server(t)
	cache := t.TempDir()

	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err == nil {
		t.Fatal("fetchBundle succeeded with a tampered manifest, want refusal")
	}
}

func TestFetchBundleTamperedAssetRejected(t *testing.T) {
	d := newFakeDaemon(t)
	d.setAssetTamper("app.js")
	srv := d.server(t)
	cache := t.TempDir()

	_, _, err := fetchBundle(srv.URL, "tok", cache, false)
	if err == nil {
		t.Fatal("fetchBundle succeeded with a tampered asset, want rejection")
	}
	if _, statErr := os.Stat(filepath.Join(cache, "console", "v1", "app.js")); statErr == nil {
		t.Error("tampered asset was written to disk despite sha256 mismatch")
	}
}

func TestFetchBundleVersionBumpRefetches(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	cache := t.TempDir()

	dirV1, m1, err := fetchBundle(srv.URL, "tok", cache, false)
	if err != nil {
		t.Fatalf("v1 fetchBundle: %v", err)
	}
	if m1.Version != "v1" {
		t.Fatalf("Version = %q, want v1", m1.Version)
	}

	d.setVersion("v2", []fakeAsset{
		{"index.html", []byte("<html><head></head><body>hello v2</body></html>")},
		{"app.js", []byte("console.log('app v2')")},
	})

	dirV2, m2, err := fetchBundle(srv.URL, "tok", cache, false)
	if err != nil {
		t.Fatalf("v2 fetchBundle: %v", err)
	}
	if m2.Version != "v2" {
		t.Fatalf("Version = %q, want v2", m2.Version)
	}
	if dirV1 == dirV2 {
		t.Fatalf("v1 and v2 cached to the same dir %q", dirV1)
	}
	if _, err := os.Stat(filepath.Join(dirV1, "index.html")); err != nil {
		t.Error("v1 cache dir was disturbed by the v2 fetch")
	}
}

func TestFetchBundleRollbackRejected(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	cache := t.TempDir()

	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err != nil {
		t.Fatalf("v1 fetchBundle: %v", err)
	}
	d.setVersion("v2", []fakeAsset{
		{"index.html", []byte("<html><head></head><body>hello v2</body></html>")},
		{"app.js", []byte("console.log('app v2')")},
	})
	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err != nil {
		t.Fatalf("v2 fetchBundle: %v", err)
	}

	// Roll the daemon back to v1 — the client must refuse it.
	d.setVersion("v1", []fakeAsset{
		{"index.html", []byte("<html><head></head><body>hello console</body></html>")},
		{"app.js", []byte("console.log('app')")},
	})
	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err == nil {
		t.Fatal("fetchBundle accepted a rollback to an older version, want refusal")
	}
}

func TestFetchBundleSizeCapTrips(t *testing.T) {
	old := maxBundleBytes
	maxBundleBytes = 8 // tiny cap; the two test assets together exceed it
	defer func() { maxBundleBytes = old }()

	d := newFakeDaemon(t)
	srv := d.server(t)
	cache := t.TempDir()

	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err == nil {
		t.Fatal("fetchBundle succeeded past the size cap, want refusal")
	}
}

func TestFetchBundleUnsignedRefusedByDefault(t *testing.T) {
	d := newFakeDaemon(t)
	d.setSigMode("missing")
	srv := d.server(t)
	cache := t.TempDir()

	if _, _, err := fetchBundle(srv.URL, "tok", cache, false); err == nil {
		t.Fatal("fetchBundle accepted an unsigned bundle with allowUnsigned=false")
	}
	if _, _, err := fetchBundle(srv.URL, "tok", cache, true); err != nil {
		t.Fatalf("fetchBundle refused an unsigned bundle with allowUnsigned=true: %v", err)
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1", "v2", true},
		{"v2", "v1", false},
		{"v1.2.3", "v1.10.0", true},
		{"v1.10.0", "v1.2.3", false},
		{"dev", "dev", false},
		{"dev", "dev2", true},
		{"v1", "v1", false},
		{"v2", "v10", true},
		{"v10", "v2", false},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
