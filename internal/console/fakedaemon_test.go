// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
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
	// realSig, when non-nil, is served verbatim for /console/manifest.sig
	// instead of a freshly-computed signature — used by newRealBundleDaemon
	// to serve the actual COMMITTED signature (internal/ui/console.manifest.sig)
	// over the actual internal/ui/web/ bundle, so Part B's e2e test proves the
	// real production signing artifact verifies, not just a same-run re-sign.
	realSig []byte

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
		realSig := d.realSig
		d.mu.Unlock()
		if realSig != nil {
			_, _ = w.Write(realSig)
			return
		}
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

	// /api/state and /events stand in for the real ui.Server's endpoints of
	// the same name (the SPA's actual state-poll and SSE-heartbeat targets —
	// see internal/ui/web/index.html and replay-player.js) — enough for the
	// e2e cookie-only test to assert the console's csrfGate lets a
	// browser-shaped request THROUGH to "the brain", without standing up the
	// full ui.Server (which needs live stores).
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.apiHits, 1)
		d.mu.Lock()
		d.lastAuth = r.Header.Get("Authorization")
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.apiHits, 1)
		d.mu.Lock()
		d.lastAuth = r.Header.Get("Authorization")
		d.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	return mux
}

// realBundleAssets walks the ACTUAL internal/ui/web/ tree (the same files
// go:embed'd into the daemon binary) and returns one fakeAsset per file, so a
// fakeDaemon can serve the real production console bundle instead of a
// synthetic stub — see newRealBundleDaemon.
func realBundleAssets(t *testing.T) []fakeAsset {
	t.Helper()
	root := filepath.Join(repoRoot(t), "internal", "ui", "web")
	var out []fakeAsset
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p) // #nosec G304 -- p is produced by WalkDir over this repo's own committed internal/ui/web/ tree, not attacker-controlled input
		if err != nil {
			return err
		}
		out = append(out, fakeAsset{path: filepath.ToSlash(rel), data: data})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/ui/web: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("internal/ui/web/ yielded no assets — fixture path likely wrong")
	}
	return out
}

// committedManifestSig reads the actual COMMITTED detached signature
// (internal/ui/console.manifest.sig) — the same bytes ui.Server's
// consoleManifestSigHandler serves in production — trimmed the same way
// ui.consoleManifestSig trims it.
func committedManifestSig(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "ui", "console.manifest.sig"))
	if err != nil {
		t.Fatalf("read internal/ui/console.manifest.sig: %v", err)
	}
	return []byte(strings.TrimSpace(string(raw)))
}

// newRealBundleDaemon is a fakeDaemon that serves the REAL, production
// internal/ui/web/ bundle (real index.html and all) at version "dev", signed
// with the actual committed dev signature — not a synthetic stub. Part B's
// e2e test uses this so it drives the console's bundleHandler + csrfGate
// against the actual served entry document a real browser would load,
// closing the gap that let three prior task-level reviews miss the
// header-vs-cookie bug: every earlier test used a synthetic bundle and a
// manually-set header, never a browser-shaped, cookie-only request through
// the real SPA.
func newRealBundleDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{
		seed:    devSeed(t),
		version: devConsoleSignedVersionForTest,
		assets:  realBundleAssets(t),
		sigMode: "valid",
		realSig: committedManifestSig(t),
	}
	// Sanity: the manifest this fixture computes from disk must be
	// byte-identical to what the daemon actually signs and serves
	// (ui.CanonicalManifestBytes) — otherwise the committed signature above
	// would fail to verify against it, and this fixture would be testing
	// nothing real. Fail loudly here rather than let that show up as an
	// opaque "signature INVALID" further down.
	want, err := ui.CanonicalManifestBytes(devConsoleSignedVersionForTest)
	if err != nil {
		t.Fatalf("ui.CanonicalManifestBytes: %v", err)
	}
	got, err := json.Marshal(d.manifest())
	if err != nil {
		t.Fatalf("marshal fixture manifest: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("fixture manifest bytes diverge from ui.CanonicalManifestBytes — internal/ui/web/ fixture is stale:\ngot:  %s\nwant: %s", got, want)
	}
	return d
}

// devConsoleSignedVersionForTest mirrors ui's unexported
// devConsoleSignedVersion ("dev") — the version the committed
// console.manifest.sig was produced for.
const devConsoleSignedVersionForTest = "dev"
