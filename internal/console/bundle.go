// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/ui"
)

// maxBundleBytes caps the total bytes fetchBundle will download for one
// console bundle version — a DoS defense against a compromised or
// misbehaving daemon serving an unbounded asset list. A var (not a const)
// so tests can shrink it rather than allocate a real 64MiB fixture.
var maxBundleBytes int64 = 64 << 20 // 64 MiB

// metaFetchLimit caps the manifest and signature fetches — small metadata,
// never legitimately near maxBundleBytes.
const metaFetchLimit = 4 << 20 // 4 MiB

// bundleHTTPTimeout bounds each manifest/asset HTTP round trip.
const bundleHTTPTimeout = 15 * time.Second

// fetchBundle fetches the daemon's signed console bundle manifest, verifies
// it (Ed25519) against the PINNED corralai release public key
// (ui.ConsoleReleasePubKeyHex — an EXTERNAL trust anchor, never taken from
// the daemon's own response), applies rollback protection, and caches every
// asset under cacheRoot/console/<version>/ (sha256-verified; re-downloading
// only what's missing or mismatched — a fully cached version does no
// network beyond the manifest+signature fetch). Returns that directory and
// the verified manifest.
//
// allowUnsigned is the dev-only escape hatch (wired by callers as
// --allow-unsigned-console): when true, a 404/absent signature is
// tolerated. A signature that IS present but fails to verify is ALWAYS
// refused, unsigned mode or not — allowUnsigned only widens what counts as
// "no signature to check," never weakens a signature that exists.
//
// Trust chain, in order: signature verify -> rollback check (per-daemon
// high-water version) -> per-asset sha256 verify. A failure at any step
// caches nothing new for that fetch — a poisoned/forged bundle from a
// compromised or forged daemon dies here.
func fetchBundle(brainRaw, token, cacheRoot string, allowUnsigned bool) (string, ui.BundleManifest, error) {
	target, err := url.Parse(brainRaw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return "", ui.BundleManifest{}, fmt.Errorf("console: invalid brain URL %q (want e.g. https://brain.example)", brainRaw)
	}

	client := &http.Client{Timeout: bundleHTTPTimeout}

	manifestBytes, status, err := fetchLimited(client, target, token, "/console/manifest.json", metaFetchLimit)
	if err != nil {
		return "", ui.BundleManifest{}, fmt.Errorf("console: fetch manifest: %w", err)
	}
	if status != http.StatusOK {
		return "", ui.BundleManifest{}, fmt.Errorf("console: fetch manifest: brain returned %d", status)
	}

	sigBytes, sigStatus, err := fetchLimited(client, target, token, "/console/manifest.sig", metaFetchLimit)
	if err != nil {
		return "", ui.BundleManifest{}, fmt.Errorf("console: fetch manifest signature: %w", err)
	}
	switch {
	case sigStatus == http.StatusOK:
		if err := verifyManifestSig(manifestBytes, sigBytes); err != nil {
			return "", ui.BundleManifest{}, fmt.Errorf("console: manifest signature INVALID — refusing to render an unverified bundle: %w", err)
		}
	case sigStatus == http.StatusNotFound && allowUnsigned:
		// Dev-only: the caller explicitly opted into an unsigned bundle.
	default:
		return "", ui.BundleManifest{}, fmt.Errorf("console: manifest is unsigned (brain returned %d for manifest.sig) — refusing to render an unverified bundle (pass --allow-unsigned-console to override for dev)", sigStatus)
	}

	var manifest ui.BundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return "", ui.BundleManifest{}, fmt.Errorf("console: decode manifest: %w", err)
	}
	if manifest.Version == "" || manifest.Entry == "" {
		return "", ui.BundleManifest{}, fmt.Errorf("console: manifest missing version/entry")
	}

	if cacheRoot == "" {
		cacheRoot = defaultCacheRoot()
	}
	consoleCacheRoot := filepath.Join(cacheRoot, "console")
	if err := os.MkdirAll(consoleCacheRoot, 0o700); err != nil {
		return "", ui.BundleManifest{}, fmt.Errorf("console: create cache root: %w", err)
	}

	if err := checkRollback(consoleCacheRoot, target.Host, manifest.Version); err != nil {
		return "", ui.BundleManifest{}, err
	}

	dir := filepath.Join(consoleCacheRoot, sanitizeVersion(manifest.Version))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", ui.BundleManifest{}, fmt.Errorf("console: create bundle dir: %w", err)
	}

	var total int64
	for _, asset := range manifest.Assets {
		if err := safeAssetPath(asset.Path); err != nil {
			return "", ui.BundleManifest{}, fmt.Errorf("console: manifest asset %q: %w", asset.Path, err)
		}
		local := filepath.Join(dir, filepath.FromSlash(asset.Path))

		if existing, err := os.ReadFile(local); err == nil && sha256Hex(existing) == asset.SHA256 { // #nosec G304 -- local is filepath.Join(dir, asset.Path) where dir is this process's own cache dir and asset.Path was just validated by safeAssetPath (rejects "..", absolute paths, any path.Clean escape) and comes from a signature-verified manifest
			total += int64(len(existing))
			if total > maxBundleBytes {
				return "", ui.BundleManifest{}, fmt.Errorf("console: bundle exceeds size cap (%d bytes)", maxBundleBytes)
			}
			continue // already cached, verified — no network for this asset
		}

		data, status, err := fetchLimited(client, target, token, "/console/asset/"+asset.Path, maxBundleBytes)
		if err != nil {
			return "", ui.BundleManifest{}, fmt.Errorf("console: fetch asset %q: %w", asset.Path, err)
		}
		if status != http.StatusOK {
			return "", ui.BundleManifest{}, fmt.Errorf("console: fetch asset %q: brain returned %d", asset.Path, status)
		}
		total += int64(len(data))
		if total > maxBundleBytes {
			return "", ui.BundleManifest{}, fmt.Errorf("console: bundle exceeds size cap (%d bytes)", maxBundleBytes)
		}
		if sha256Hex(data) != asset.SHA256 {
			return "", ui.BundleManifest{}, fmt.Errorf("console: asset %q failed sha256 verification — refusing to cache", asset.Path)
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
			return "", ui.BundleManifest{}, fmt.Errorf("console: create asset dir: %w", err)
		}
		if err := os.WriteFile(local, data, 0o644); err != nil { // #nosec G306 -- 0644 is the correct, intended perm: these are public UI assets meant to be served back out over HTTP, not secret; the directory tree itself is 0700
			return "", ui.BundleManifest{}, fmt.Errorf("console: write asset %q: %w", asset.Path, err)
		}
	}

	if err := writeHighWaterMark(consoleCacheRoot, target.Host, manifest.Version); err != nil {
		return "", ui.BundleManifest{}, fmt.Errorf("console: persist rollback high-water mark: %w", err)
	}

	return dir, manifest, nil
}

// fetchLimited GETs brain+path with the bearer, refusing to buffer more than
// limit+1 bytes (so a pathological/compromised daemon can't exhaust client
// memory via one oversized response).
func fetchLimited(client *http.Client, target *url.URL, token, reqPath string, limit int64) ([]byte, int, error) {
	u := *target
	u.Path = strings.TrimRight(target.Path, "/") + reqPath
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Host = target.Host
	req.Header.Set("Authorization", "Bearer "+token) // never logged
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) > limit {
		return nil, 0, fmt.Errorf("response for %s exceeds %d byte limit", reqPath, limit)
	}
	return data, resp.StatusCode, nil
}

// verifyManifestSig checks the detached hex Ed25519 signature over
// manifestBytes against the PINNED ui.ConsoleReleasePubKeyHex — a
// build-time constant external to anything the daemon serves. This is the
// trust anchor: a daemon (or a MITM) cannot supply its own key and have a
// client trust it.
func verifyManifestSig(manifestBytes, sigBytes []byte) error {
	pubBytes, err := hex.DecodeString(ui.ConsoleReleasePubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("pinned console release public key is invalid (build/config bug): decode err=%v len=%d", err, len(pubBytes))
	}
	sig, err := hex.DecodeString(strings.TrimSpace(string(sigBytes)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature is not valid hex of the expected size")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), manifestBytes, sig) {
		return fmt.Errorf("signature does not verify against the pinned release key")
	}
	return nil
}

// sha256Hex is the hex sha256 digest of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// safeAssetPath rejects any manifest asset path that could escape the
// per-version cache directory — belt-and-suspenders on top of the
// signature verification (a signed manifest is trusted, but "trusted"
// should never mean "unvalidated").
func safeAssetPath(p string) error {
	if p == "" || path.IsAbs(p) || strings.Contains(p, "..") {
		return fmt.Errorf("unsafe asset path")
	}
	if clean := path.Clean(p); clean != p {
		return fmt.Errorf("unsafe asset path")
	}
	return nil
}

// sanitizeVersion turns an (already signature-verified) manifest version
// into a safe cache-directory component — defense in depth against a
// version string crafted to escape cacheRoot.
func sanitizeVersion(v string) string {
	clean := path.Clean(v)
	clean = strings.ReplaceAll(clean, "/", "_")
	clean = strings.ReplaceAll(clean, "\\", "_")
	if clean == "" || clean == "." || clean == ".." {
		clean = "invalid-version"
	}
	return clean
}

// hostKey derives a filesystem-safe, per-daemon key (for the rollback
// high-water-mark file) from the brain's host:port.
func hostKey(host string) string {
	sum := sha256.Sum256([]byte(host))
	return hex.EncodeToString(sum[:16])
}

// checkRollback enforces TUF-style rollback protection: refuse a manifest
// version older than the last one this client accepted for this daemon.
func checkRollback(consoleCacheRoot, host, version string) error {
	prev, err := os.ReadFile(hwmPath(consoleCacheRoot, host))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // first time seeing this daemon
		}
		return fmt.Errorf("console: read rollback high-water mark: %w", err)
	}
	prevVersion := strings.TrimSpace(string(prev))
	if versionLess(version, prevVersion) {
		return fmt.Errorf("console: refusing rollback — daemon offered version %q, last accepted %q for this brain", version, prevVersion)
	}
	return nil
}

// writeHighWaterMark persists version as the new rollback floor for host.
// Only called after every asset for this fetch has been verified and
// written — never on a partially-verified fetch.
func writeHighWaterMark(consoleCacheRoot, host, version string) error {
	return os.WriteFile(hwmPath(consoleCacheRoot, host), []byte(version), 0o600)
}

func hwmPath(consoleCacheRoot, host string) string {
	return filepath.Join(consoleCacheRoot, hostKey(host)+".hwm")
}

// versionLess reports whether a is an older version than b, using a
// pragmatic segment-wise comparison (splitting on '.', '-', '+'): numeric
// segments compare numerically, non-numeric segments compare as strings.
// This handles semver-ish tags (v1.2.3) and the non-semver
// "dev"/git-sha strings corral also stamps as a version, without requiring
// strict semver — an unparseable-but-different version still gets a
// deterministic (lexicographic) ordering rather than silently passing
// rollback protection.
func versionLess(a, b string) bool {
	if a == b {
		return false
	}
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var sa, sb string
		if i < len(as) {
			sa = as[i]
		}
		if i < len(bs) {
			sb = bs[i]
		}
		if sa == sb {
			continue
		}
		na, aerr := strconv.Atoi(sa)
		nb, berr := strconv.Atoi(sb)
		if aerr == nil && berr == nil {
			return na < nb
		}
		return sa < sb
	}
	return false
}

func splitVersion(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
}

// defaultCacheRoot is cacheRoot's default: os.UserCacheDir()/corral,
// falling back to os.TempDir()/corral if the user cache dir can't be
// determined (e.g. minimal container environments).
func defaultCacheRoot() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "corral")
}
