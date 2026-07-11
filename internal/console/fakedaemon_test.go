// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/ui"
)

// devSeed reads the committed dev Ed25519 signing seed
// (scripts/dev-console-signing-key.hex) — its public half is the PINNED
// ui.ConsoleReleasePubKeyHex these tests verify against, so it's the only
// key that can produce a signature fetchBundle will accept.
func devSeed(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "dev-console-signing-key.hex"))
	if err != nil {
		t.Fatalf("read dev signing key: %v", err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("dev signing key invalid: err=%v len=%d", err, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/console -> repo root is two levels up.
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}

type fakeAsset struct {
	path string
	data []byte
}

// fakeDaemon is a minimal stand-in for the Task-1 daemon's /console/*
// endpoints (+ a stub /api/ping), with knobs to simulate a tampered
// manifest, tampered/missing/foreign-key signature, tampered asset, and a
// version bump — every failure mode fetchBundle must refuse.
type fakeDaemon struct {
	seed ed25519.PrivateKey

	mu              sync.Mutex
	version         string
	assets          []fakeAsset
	sigMode         string // "valid" (default), "missing", "tampered", "wrongkey"
	manifestTamper  bool   // serve manifest.json bytes that differ from what was signed
	assetTamperPath string // corrupt this one asset's served bytes

	apiHits   int32
	assetHits int32
	lastAuth  string
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	return &fakeDaemon{
		seed:    devSeed(t),
		version: "v1",
		assets: []fakeAsset{
			{"index.html", []byte("<html><head></head><body>hello console</body></html>")},
			{"app.js", []byte("console.log('app')")},
		},
		sigMode: "valid",
	}
}

func (d *fakeDaemon) manifest() ui.BundleManifest {
	d.mu.Lock()
	defer d.mu.Unlock()
	assets := make([]ui.BundleAsset, len(d.assets))
	for i, a := range d.assets {
		sum := sha256.Sum256(a.data)
		assets[i] = ui.BundleAsset{Path: a.path, SHA256: hex.EncodeToString(sum[:])}
	}
	return ui.BundleManifest{Version: d.version, Entry: "index.html", Assets: assets}
}

func (d *fakeDaemon) setVersion(v string, assets []fakeAsset) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.version = v
	d.assets = assets
}

func (d *fakeDaemon) setSigMode(mode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sigMode = mode
}

func (d *fakeDaemon) setManifestTamper(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.manifestTamper = on
}

func (d *fakeDaemon) setAssetTamper(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.assetTamperPath = path
}

func (d *fakeDaemon) server(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(d.handler())
	t.Cleanup(s.Close)
	return s
}

func (d *fakeDaemon) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/console/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		m := d.manifest()
		b, err := json.Marshal(m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.mu.Lock()
		tamper := d.manifestTamper
		d.mu.Unlock()
		if tamper {
			// Served bytes differ from what manifest.sig below signs, so
			// signature verification must fail.
			b = append(b, ' ')
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/console/manifest.sig", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		mode := d.sigMode
		d.mu.Unlock()
		if mode == "missing" {
			http.NotFound(w, r)
			return
		}
		mb, err := json.Marshal(d.manifest()) // always the TRUE (untampered) canonical bytes
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var sig []byte
		switch mode {
		case "tampered":
			sig = ed25519.Sign(d.seed, mb)
			sig[0] ^= 0xFF
		case "wrongkey":
			_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			sig = ed25519.Sign(wrongPriv, mb)
		default: // "valid"
			sig = ed25519.Sign(d.seed, mb)
		}
		_, _ = w.Write([]byte(hex.EncodeToString(sig)))
	})

	mux.HandleFunc("/console/asset/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.assetHits, 1)
		p := strings.TrimPrefix(r.URL.Path, "/console/asset/")
		d.mu.Lock()
		var data []byte
		found := false
		for _, a := range d.assets {
			if a.path == p {
				data = append([]byte{}, a.data...)
				found = true
				break
			}
		}
		tamperPath := d.assetTamperPath
		d.mu.Unlock()
		if !found {
			http.NotFound(w, r)
			return
		}
		if p == tamperPath {
			data = append(data, []byte("TAMPERED")...)
		}
		_, _ = w.Write(data)
	})

	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.apiHits, 1)
		d.mu.Lock()
		d.lastAuth = r.Header.Get("Authorization")
		d.mu.Unlock()
		_, _ = w.Write([]byte("pong"))
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	return mux
}
