// SPDX-License-Identifier: Elastic-2.0

// Command corral is the CorralAI "brain": an OIDC-authenticated, MCP-native
// coordination server that thin clients (coding agents on any machine) connect
// to over streamable-HTTP.
//
//	corral   # serves /mcp/ + /healthz on $CORRALAI_ADDR
//
// Env:
//
//	CORRALAI_ADDR              listen address (default 127.0.0.1:9019)
//	CORRALAI_DB                coordination SQLite path (default ~/.claude/corralai_coord.sqlite3)
//	CORRALAI_MEMORY_DB         memory DuckDB path (default ~/.claude/corralai_memory.duckdb)
//	CORRALAI_RECORDINGS_DB     recordings DuckDB path for scrubbed replay exports (default ~/.claude/corralai_recordings.duckdb)
//	CORRALAI_MEMORY_DIR        where new memory entries are written (default ~/.claude/projects/default/memory)
//	CORRALAI_PROJECT_TIERS     optional path->tier rules "substr=tier,substr=tier"; front-matter project: wins, else "default"
//	CORRALAI_OIDC_ISSUER       OIDC issuer URL (any OIDC provider: Keycloak, Auth0, Okta, Dex, Authentik, …); empty => AUTH DISABLED (dev)
//	CORRALAI_OIDC_AUDIENCE     expected token aud (the client_id)
//	CORRALAI_OIDC_CLIENTS      extra trusted clients "issuer|aud,issuer|aud"
//	CORRALAI_ALLOWED_PRINCIPALS day-0 SEED of member emails (DB is canonical after; empty => any authenticated)
//	CORRALAI_PRINCIPALS_DB     role/allowlist SQLite path (default ~/.claude/corralai_principals.sqlite3)
//	CORRALAI_MEMORY_OWNERS     comma list of emails allowed to read/write memory (empty => any authorized)
//	CORRALAI_ALLOWED_HOSTS     comma list of accepted Host headers (default: the brain's domains + localhost)
//	CORRALAI_CLIENT_IP_HEADER  trusted real-client-IP header for rate limiting (e.g. CF-Connecting-IP); empty => RemoteAddr
//	CORRALAI_RATELIMIT_IP_PER_MIN / _IP_BURST       per-IP rate limit (default 300/min, burst 100)
//	CORRALAI_RATELIMIT_USER_PER_MIN / _USER_BURST   per-principal rate limit (default 600/min, burst 200)
//	CORRALAI_MAX_BODY_BYTES    max request body size (default 1 MiB)
//	CORRALAI_TLS_CERT / _KEY   serve HTTPS with these PEM files (built-in TLS, no proxy needed)
//	CORRALAI_TLS_AUTOCERT_DOMAINS  comma list => auto Let's Encrypt certs (needs public reachability)
//	CORRALAI_TLS_AUTOCERT_CACHE    cert cache dir (default ~/.cache/corralai-autocert)
//	CORRALAI_ADMIN_PRINCIPALS  day-0 SEED of superuser emails (DB is canonical after; `corral createsuperuser` adds more)
//	CORRALAI_GATEWAY_DB        MCP-gateway registry SQLite path (default ~/.claude/corralai_gateway.sqlite3)
//	CORRALAI_ARTIFACTS_DB      fleet skill/hook sync SQLite path (default ~/.claude/corralai_artifacts.sqlite3)
//	CORRALAI_GATEWAY_ALLOWED_HOSTS  hosts the gateway may dial despite the SSRF block (private/internal targets); empty => block all private/loopback
//	CORRALAI_MOTHERDUCK                  fleet-sync target: "md:<db>" or a .duckdb path; empty => sync off
//	CORRALAI_MOTHERDUCK_TOKEN            MotherDuck token (exported as motherduck_token for md: attach)
//	CORRALAI_BRAIN_ID                    tag for this brain's rows (default hostname)
//	CORRALAI_SYNC_INTERVAL               fleet sync interval, seconds (default 30)
//	CORRALAI_FLEET_RETENTION_DISABLE     set "1" to disable the retention/compaction cycle entirely
//	CORRALAI_FLEET_RETENTION_DAYS        TTL window in days (default 90; 0 = TTL off, compaction still runs)
//	CORRALAI_FLEET_RETENTION_INTERVAL_SEC  how often (seconds) to run the retention cycle (default 3600)
//	CORRALAI_GIT_TOKEN         GitHub PAT for repo-work missions (clone + PR); empty => repo engine disabled unless CORRALAI_REPO_ENABLE=1
//	CORRALAI_GITHUB_API        GitHub API base URL (default https://api.github.com)
//	CORRALAI_REPO_WORKSPACE    root dir for per-mission working copies (default $TMPDIR/corral-repos)
//	CORRALAI_REPO_ENABLE       set "1" to enable the repo engine even without a token (anonymous / GitHub Apps token flow)
//	CORRALAI_REVIEW_POLL_SEC   how often (seconds) the brain polls open PRs for CHANGES_REQUESTED reviews (default 60)
//	CORRALAI_BRAIN_KEY         base64-encoded Ed25519 seed (32 bytes) for cross-swarm brain identity; takes priority over key file
//	CORRALAI_BRAIN_KEY_FILE    path to persist the brain key seed (default ~/.claude/corralai_brain_key); created 0600 on first run
//	CORRALAI_BRAIN_PEERS       optional allowlist "brain_id:pubB64" entries (comma or newline separated); empty => TOFU mode
//	CORRALAI_LEARN_DB          learning-loop proposals SQLite path (default ~/.claude/corralai_learn.sqlite3)
//	CORRALAI_LEARN_SWEEP_SECONDS  how often (seconds) the learn sweep clusters findings/lessons into proposals (default 60)
//	CORRALAI_BUILD_DB          `corral certify` signed build-record ledger DuckDB path (default ~/.claude/corralai_build.duckdb)
//	CORRALAI_CERTIFY_KEY       hex-encoded Ed25519 seed (32 bytes) `corral certify` build attestations are signed with; takes priority over key file
//	CORRALAI_CERTIFY_KEY_FILE  path to persist the certify signing key seed (default ~/.claude/corralai_certify_key); created 0600 on first run
//	CORRALAI_BRAIN_TOKEN       `corral certify`'s bearer token to authenticate to a brain (via `corral secret set`); distinct
//	                           from CORRALAI_BRAIN_KEY above (that's an Ed25519 IDENTITY SEED, not a bearer token — do not reuse it)
//	CORRALAI_REKOR_URL         Sigstore Rekor instance report_build anchors signed build attestations to (default https://rekor.sigstore.dev);
//	                           `corral certify verify` checks the same default unless --rekor-url overrides it
//	CORRALAI_GATE_POLICIES     repo merge gate: ";"-separated policies "repo=owner/name,base=main,net=false,timeout=600,cmd=go test ./...";
//	                           cmd= MUST be the last field — everything after it is the command verbatim (commas
//	                           allowed, never split) so "cmd=go test -run A,B ./..." isn't silently truncated;
//	                           timeout= is seconds, defaults to gate.DefaultGateTimeout (600s) when omitted;
//	                           empty => the repo gate is OFF (no poller starts); GitHub-only for v1
//	CORRALAI_GATE_DB           repo gate dedupe/index store DuckDB path (default ~/.claude/corralai_gate.duckdb)
//	CORRALAI_GATE_POLL_SECONDS how often (seconds) the repo gate polls covered repos for new PR heads (default 120)
//	CORRALAI_GATE_EXEC_BACKEND / _EXEC_UNSAFE_HOST  same jail backend used by the independent verify-gate (see below);
//	                           the repo gate reuses it — a missing backend disables the repo gate too, loudly, never unsandboxed
//	CORRALAI_CONTROL_GATE     control gate: ";"-separated "repo=owner/name,owner=<principal>,lang=go,base=main"
//	                           — runs the owner's VETTED tests against PR heads, posts corral/control-gate
//	CORRALAI_CONTROL_GATE_SPEC_DB  control-gate vetted-tests store (default ~/.claude/corralai_control_spec.duckdb)
//	CORRALAI_CONTROL_GATE_DB       control-gate dedupe/index store (default ~/.claude/corralai_control_gate.duckdb)
//	CORRALAI_CONTROL_GATE_POLL_SECONDS  how often the control gate polls for new PR heads (default 120)
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/attest"
	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/egress"
	"github.com/pdbethke/corralai/internal/embed"
	"github.com/pdbethke/corralai/internal/fleet"
	"github.com/pdbethke/corralai/internal/gate"
	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/limit"
	"github.com/pdbethke/corralai/internal/llm"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/oracle"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/recordings"
	"github.com/pdbethke/corralai/internal/reference"
	"github.com/pdbethke/corralai/internal/repo"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/taskartifacts"
	"github.com/pdbethke/corralai/internal/telemetry"
	"github.com/pdbethke/corralai/internal/transparency"
	"github.com/pdbethke/corralai/internal/ui"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// subcommand reports which known corral subcommand args (shaped like
// os.Args[1:]) names, or "" if none. It is checked BEFORE showVersion/
// showHelp scan every arg for -v/-h/version: those scans previously ran
// first and saw INTO the checked command's own argv, so `corral certify --
// go test -v ./...` matched the -v after "--" and printed the version,
// exiting 0 WITHOUT ever running the check — a silent false pass. Dispatch
// by args[0] alone sidesteps that: only the subcommand name itself is
// examined, never anything after it.
func subcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "certify", "secret", "control":
		return args[0]
	}
	return ""
}

// showVersion reports whether the args ask for the version.
func showVersion(args []string) bool {
	for _, a := range args {
		if a == "--version" || a == "-version" || a == "version" || a == "-v" {
			return true
		}
	}
	return false
}

// showHelp reports whether the args ask for usage. Checked before the server
// starts: without it, `corral -h` fell through into main()'s server startup
// and hung forever instead of exiting — the exact "docs generator can't
// capture text a binary refuses to print" bug the CLI reference generator
// exists to catch, just with a hang instead of empty output.
func showHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}

// usageText is a short summary plus a pointer to the full env-var reference
// already documented in this file's top-of-file doc comment (kept there as
// the single source of truth scripts/gen-cli-docs.sh extracts from).
func usageText() string {
	return `corral — the CorralAI brain: an OIDC-authenticated, MCP-native coordination server

Usage:
  corral                          serve /mcp/ + /healthz on $CORRALAI_ADDR
  corral secret set|get|list|rm   manage provider keys + tokens in the secure keystore
                                  (env → OS keyring → age-encrypted file; set reads stdin, never argv)
  corral control seed [flags]     seed one vetted control test into the control-gate store
                                  (--spec-db --owner --goal --target --code-path --test-path --test-file)
  corral certify --brain <url> [flags] -- <command>...
                                  run <command>, sign + record the result as a tamper-evident
                                  build attestation on the brain (report_build); exits with
                                  <command>'s own exit code
                                  flags: --produced-by a,b   --out <file>
                                         --repo/--commit/--branch (default: read via git)
  corral certify verify <record-file> [--pubkey <hex>|--brain <url>]
                                  independently verify a --out (or report_build) record: the
                                  Ed25519 signature, the ledger's hash chain, and that the
                                  statement is bound to that exact ledger head — requires a
                                  trusted key via --pubkey or --brain (a record's own
                                  embedded public_key is never a trust anchor); prints
                                  "verified" and exits 0, or names the failing check on
                                  stderr and exits non-zero
  corral --version                print the build version and exit
  corral -h                       print this help and exit

Configuration is entirely environment variables — see CORRALAI_ADDR,
CORRALAI_DB, and the rest of the // Env: block at the top of this binary's
main.go (also reproduced in the generated CLI reference).
`
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// delegationKey resolves the HMAC key for subagent delegation tokens:
// CORRALAI_DELEGATION_SECRET, else a systemd credential (delegation-secret), else
// a random ephemeral key (tokens then don't survive a restart — fine for the
// short-lived subagents they're meant for).
func delegationKey() []byte {
	if v := os.Getenv("CORRALAI_DELEGATION_SECRET"); v != "" {
		scrubSecrets([]string{"CORRALAI_DELEGATION_SECRET"}) // consumed; scrub before any in-process reader can see it
		return []byte(v)
	}
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		if b, err := os.ReadFile(filepath.Join(d, "delegation-secret")); err == nil { // #nosec G703,G304 -- reads a fixed-name systemd credential from $CREDENTIALS_DIRECTORY (operator-trusted env), not attacker input
			if s := strings.TrimSpace(string(b)); len(s) >= 16 {
				return []byte(s)
			}
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		log.Fatalf("delegation key: %v", err)
	}
	log.Printf("delegation: secret unset — random ephemeral key (subagent tokens drop on restart)")
	return k
}

// splitList parses a comma-separated env value into trimmed, non-empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseBrainPeers parses CORRALAI_BRAIN_PEERS into a brain_id→pubB64 allowlist.
// Returns nil (TOFU mode) when s is empty or yields no valid entries.
// Format: "brain_id:pubB64" entries separated by commas or newlines.
func parseBrainPeers(s string) map[string]string {
	if s == "" {
		return nil
	}
	// Normalise: treat newlines as commas so both $'...' and file-sourced values work.
	s = strings.ReplaceAll(s, "\n", ",")
	out := make(map[string]string)
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.Index(entry, ":")
		if idx <= 0 {
			log.Printf("brain peers: malformed entry (expected brain_id:pubB64): %q — skipped", entry)
			continue
		}
		brainID := strings.TrimSpace(entry[:idx])
		pubB64 := strings.TrimSpace(entry[idx+1:])
		if brainID == "" || pubB64 == "" {
			continue
		}
		out[brainID] = pubB64
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scrubSecrets removes each key from the process environment. Call this after a
// secret has been loaded into its owning struct so no in-process reader (DuckDB
// getenv in the fleet oracle, an os.Environ dump, a subprocess) can retrieve it
// afterward. motherduck_token (lowercase) is intentionally NOT included: the fleet
// sync and oracle need it for the md: attach; it is the lower-value reporting
// credential, never the git PAT.
func scrubSecrets(keys []string) {
	for _, k := range keys {
		os.Unsetenv(k)
	}
}

func authzDesc(n int) string {
	if n == 0 {
		return "open (any authenticated)"
	}
	return strconv.Itoa(n) + " allowed principal(s)"
}

// listen serves the brain with TLS when configured (so it's secure standalone,
// no TLS-terminating proxy required), else plain HTTP (for behind a tunnel/proxy):
//   - CORRALAI_TLS_CERT + CORRALAI_TLS_KEY: serve HTTPS with those PEM files.
//   - CORRALAI_TLS_AUTOCERT_DOMAINS: auto-obtain/renew Let's Encrypt certs
//     (TLS-ALPN-01 on :443; needs the brain publicly reachable on the domain).
//   - neither: plain HTTP (default).
func listen(httpSrv *http.Server) error {
	base := &tls.Config{MinVersion: tls.VersionTLS12}
	cert, key := os.Getenv("CORRALAI_TLS_CERT"), os.Getenv("CORRALAI_TLS_KEY")
	domains := splitList(os.Getenv("CORRALAI_TLS_AUTOCERT_DOMAINS"))
	switch {
	case cert != "" && key != "":
		httpSrv.TLSConfig = base
		log.Printf("TLS: HTTPS with cert file %s", cert) // #nosec G706 -- operator-controlled config, not user input
		return httpSrv.ListenAndServeTLS(cert, key)
	case len(domains) > 0:
		home, _ := os.UserHomeDir()
		cacheDir := env("CORRALAI_TLS_AUTOCERT_CACHE", filepath.Join(home, ".cache", "corralai-autocert"))
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domains...),
			Cache:      autocert.DirCache(cacheDir),
		}
		tc := m.TLSConfig()
		tc.MinVersion = tls.VersionTLS12
		httpSrv.TLSConfig = tc
		log.Printf("TLS: auto (Let's Encrypt) for %v (cache %s)", domains, cacheDir)
		return httpSrv.ListenAndServeTLS("", "")
	default:
		log.Printf("TLS: disabled — serving plain HTTP (front with a TLS proxy/tunnel)")
		return httpSrv.ListenAndServe()
	}
}

// hostAllow rejects requests whose Host header isn't in the allowlist (defense
// against DNS-rebinding / Host-spoofing on the MCP endpoint).
func hostAllow(hosts []string, next http.Handler) http.Handler {
	set := map[string]bool{}
	for _, h := range hosts {
		set[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !set[strings.ToLower(host)] {
			http.Error(w, "forbidden: host not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startFleetSync periodically replicates the curated reporting set to MotherDuck
// (or any remote DuckDB) for fleet observability — when CORRALAI_MOTHERDUCK is set.
// Retention (Compact) runs in the SAME goroutine on a coarse cadence so it is
// serialized with Sync and never runs concurrently with it.
// startFleetSync periodically replicates the curated reporting set to MotherDuck
// (or any remote DuckDB) for fleet observability — when CORRALAI_MOTHERDUCK is set.
// Retention (Compact) runs in the SAME goroutine on a coarse cadence so it is
// serialized with Sync and never runs concurrently with it.
//
// registrationRetry, when non-nil, is called on each tick until it returns true.
// Use this to retry a startup brain registration that failed transiently (e.g.
// MotherDuck unreachable at boot). The retry runs BEFORE Sync so registration is
// attempted as early as possible. Once the function returns true it is not called
// again. Callers must set motherduck_token in the environment before calling
// startFleetSync (via initMotherDuckToken), since both registration and Sync need it.
func startFleetSync(cfg fleet.SyncConfig, registrationRetry func() bool) {
	target := os.Getenv("CORRALAI_MOTHERDUCK") // e.g. "md:corralai" or a .duckdb path
	if target == "" {
		log.Printf("fleet: MotherDuck sync disabled (set CORRALAI_MOTHERDUCK to enable)")
		return
	}
	host, _ := os.Hostname()
	brainID := env("CORRALAI_BRAIN_ID", host)
	interval := 30 * time.Second
	if s := os.Getenv("CORRALAI_SYNC_INTERVAL"); s != "" {
		if d, err := strconv.Atoi(s); err == nil && d > 0 {
			interval = time.Duration(d) * time.Second
		}
	}

	// Retention config is read once at startup. If disabled, skip scheduling it
	// entirely — no counter overhead, no log noise on every tick.
	retCfg := fleet.RetentionConfigFromEnv()
	retentionEvery := 0 // 0 = retention not scheduled
	if !retCfg.Disabled {
		retIntervalSec := envInt("CORRALAI_FLEET_RETENTION_INTERVAL_SEC", 3600)
		syncSec := int(interval.Seconds())
		if syncSec < 1 {
			syncSec = 1
		}
		retentionEvery = retIntervalSec / syncSec
		if retentionEvery < 1 {
			retentionEvery = 1
		}
		log.Printf("fleet: retention enabled (TTL=%dd, every ~%ds / %d sync ticks, brain=%s)",
			retCfg.TTLDays, retIntervalSec, retentionEvery, brainID)
	} else {
		log.Printf("fleet: retention disabled (CORRALAI_FLEET_RETENTION_DISABLE=1)")
	}

	log.Printf("fleet: syncing curated reporting set to %s every %s (brain=%s)", target, interval, brainID) // #nosec G706 -- operator-controlled config, not user input
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		var tick int
		regDone := registrationRetry == nil // nil means no retry needed
		for range t.C {
			// Registration retry: if startup registration failed transiently (MotherDuck
			// was briefly unreachable), retry here — serialized with Sync so there is no
			// concurrent write race. Once registrationRetry returns true we stop calling it.
			if !regDone {
				regDone = registrationRetry()
			}
			if n, err := fleet.Sync(cfg, target, brainID); err != nil {
				log.Printf("fleet: sync error: %v", err)
			} else if n > 0 {
				log.Printf("fleet: synced %d reporting rows to %s", n, target) // #nosec G706 -- operator-controlled config, not user input
			}
			// Compact runs in this same goroutine (serialized with Sync) on a coarse
			// cadence. retentionEvery==0 means retention was disabled at startup.
			if retentionEvery > 0 && tick%retentionEvery == 0 {
				if del, err := fleet.Compact(retCfg, target, brainID, time.Now()); err != nil {
					log.Printf("fleet retention cycle error: %v", err)
				} else if len(del) > 0 {
					log.Printf("fleet retention: %v", del) // #nosec G706 -- operator-controlled config, not user input
				}
			}
			tick++
		}
	}()
}

// initMotherDuckToken promotes CORRALAI_MOTHERDUCK_TOKEN → motherduck_token (lowercase),
// which DuckDB reads when opening md: attachments, then scrubs the uppercase form.
// Must be called before any fleet.RegisterBrain or fleet.Sync that targets md:.
func initMotherDuckToken() {
	if tok := os.Getenv("CORRALAI_MOTHERDUCK_TOKEN"); tok != "" {
		_ = os.Setenv("motherduck_token", tok)              // DuckDB reads lowercase when attaching md:
		scrubSecrets([]string{"CORRALAI_MOTHERDUCK_TOKEN"}) // scrub uppercase; lowercase stays for md: attach
	}
}

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Dispatch known subcommands BEFORE the version/help scan — see
	// subcommand's doc comment for why the order matters (silent-green fix).
	switch subcommand(os.Args[1:]) {
	case "secret":
		if err := runSecret(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "corral secret:", err)
			os.Exit(1)
		}
		return
	case "certify":
		os.Exit(runCertify(os.Args[2:], realRunner{}, mcpPoster{}, os.Stdout, os.Stderr))
	case "control":
		if err := runControl(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "corral control:", err)
			os.Exit(1)
		}
		return
	}
	if showVersion(os.Args[1:]) {
		log.SetFlags(0)
		log.Println("corral", version)
		return
	}
	if showHelp(os.Args[1:]) {
		fmt.Print(usageText())
		return
	}
	home, _ := os.UserHomeDir()
	addr := env("CORRALAI_ADDR", "127.0.0.1:9019")
	dbPath := env("CORRALAI_DB", filepath.Join(home, ".claude", "corralai_coord.sqlite3"))
	issuer := os.Getenv("CORRALAI_OIDC_ISSUER")
	audience := os.Getenv("CORRALAI_OIDC_AUDIENCE")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Fatalf("mkdir db dir: %v", err)
	}
	store, err := coord.Open(dbPath)
	if err != nil {
		log.Fatalf("open coordination store: %v", err)
	}
	defer store.Close()
	bus := coord.NewBus() // signal bus: coordination actions -> instant SSE push
	store.SetBus(bus)

	memDB := env("CORRALAI_MEMORY_DB", filepath.Join(home, ".claude", "corralai_memory.duckdb"))
	memStore, err := memory.Open(memDB)
	if err != nil {
		log.Fatalf("open memory store: %v", err)
	}
	defer memStore.Close()
	memStore.SetProjectTiers(os.Getenv("CORRALAI_PROJECT_TIERS"))

	// Memory corpus is private to its owners (verified principals); empty => any
	// authorized caller may read it.
	memOwners := map[string]bool{}
	for _, p := range splitList(os.Getenv("CORRALAI_MEMORY_OWNERS")) {
		memOwners[p] = true
	}
	// Django-style principal/role store: who may use the brain + who is a superuser.
	// The DB is canonical at runtime; CORRALAI_ADMIN_PRINCIPALS (superusers) and
	// CORRALAI_ALLOWED_PRINCIPALS (members) are a day-0 SEED only. `corral
	// createsuperuser` writes here. Empty table => dev-open.
	princDB := env("CORRALAI_PRINCIPALS_DB", filepath.Join(home, ".claude", "corralai_principals.sqlite3"))
	princStore, err := principals.Open(princDB)
	if err != nil {
		log.Fatalf("open principals store: %v", err)
	}
	defer princStore.Close()
	if n, err := princStore.Seed(splitList(os.Getenv("CORRALAI_ADMIN_PRINCIPALS")), splitList(os.Getenv("CORRALAI_ALLOWED_PRINCIPALS"))); err != nil {
		log.Fatalf("seed principals: %v", err)
	} else if n > 0 {
		log.Printf("principals: seeded %d from env (day-0)", n)
	}
	log.Printf("principals: %d total, %d superuser(s)", princStore.Count(), princStore.SuperuserCount())

	// MCP gateway registry (admin-curated upstreams + audited proxy).
	gwDB := env("CORRALAI_GATEWAY_DB", filepath.Join(home, ".claude", "corralai_gateway.sqlite3"))
	gwStore, err := gateway.Open(gwDB)
	if err != nil {
		log.Fatalf("open gateway store: %v", err)
	}
	defer gwStore.Close()

	// SSRF guard for gateway egress: blocks the brain from dialing private/loopback/
	// link-local targets unless an admin allowlists the host.
	egress := gateway.NewGuard(splitList(os.Getenv("CORRALAI_GATEWAY_ALLOWED_HOSTS")))

	// Fleet artifact store: shared skills + hooks, bidirectionally synced by
	// `corral sync` (superuser push). The brain is the canonical set.
	artDB := env("CORRALAI_ARTIFACTS_DB", filepath.Join(home, ".claude", "corralai_artifacts.sqlite3"))
	artStore, err := artifacts.Open(artDB)
	if err != nil {
		log.Fatalf("open artifacts store: %v", err)
	}
	defer artStore.Close()
	log.Printf("artifacts: %d shared skill/hook file(s), head rev %d", artStore.Count(), artStore.HeadRev())

	// Orchestration: a directive becomes a mission the engine drives over the
	// subagent + instruction primitives (build -> test ∥ secops -> retro).
	missionDB := env("CORRALAI_MISSION_DB", filepath.Join(home, ".claude", "corralai_missions.sqlite3"))
	missionStore, err := mission.Open(missionDB)
	if err != nil {
		log.Fatalf("open mission store: %v", err)
	}
	defer missionStore.Close()
	// Task queue: the pull-model substrate the swarm executes missions through.
	queueDB := env("CORRALAI_QUEUE_DB", filepath.Join(home, ".claude", "corralai_queue.sqlite3"))
	queueStore, err := queue.Open(queueDB)
	if err != nil {
		log.Fatalf("open task queue: %v", err)
	}
	defer queueStore.Close()

	// Reference corpus (RAG): embedded DuckDB store + a remote embeddings endpoint.
	// A nil embedder (no CORRALAI_EMBED_URL) keeps the reference tools off — the
	// engine is portable: embeddings come from wherever you point it, not this host.
	refDB := env("CORRALAI_REFERENCE_DB", filepath.Join(home, ".claude", "corralai_reference.duckdb"))
	refStore, err := reference.Open(refDB)
	if err != nil {
		log.Fatalf("open reference store: %v", err)
	}
	defer refStore.Close()
	embedder := embed.New() // one client → one vector space for reference AND memory
	memStore.SetEmbedder(embedder)
	if n, err := memStore.Build(nil); err != nil {
		log.Printf("memory: index build warning: %v", err)
	} else {
		log.Printf("memory: indexed %d entries (FTS=%v)", n, memStore.FTS())
	}
	if embedder != nil {
		log.Printf("reference: RAG enabled (embeddings via %s)", os.Getenv("CORRALAI_EMBED_URL")) // #nosec G706 -- operator-controlled config, not user input
	} else {
		log.Printf("reference: store open; embeddings disabled (set CORRALAI_EMBED_URL to enable RAG)")
	}

	// Mission telemetry: the DuckDB event log for analytics.
	telDB := env("CORRALAI_TELEMETRY_DB", filepath.Join(home, ".claude", "corralai_telemetry.duckdb"))
	telStore, err := telemetry.Open(telDB)
	if err != nil {
		log.Fatalf("open telemetry store: %v", err)
	}
	defer telStore.Close()
	recDB := env("CORRALAI_RECORDINGS_DB", filepath.Join(home, ".claude", "corralai_recordings.duckdb"))
	recStore, err := recordings.Open(recDB)
	if err != nil {
		log.Fatalf("open recordings store: %v", err)
	}
	defer recStore.Close()

	// Learning loop: findings + lessons cluster into human-gated proposals
	// (approve into standing guidance, optionally a skill).
	learnDB := env("CORRALAI_LEARN_DB", filepath.Join(home, ".claude", "corralai_learn.sqlite3"))
	learnStore, err := learn.Open(learnDB)
	if err != nil {
		log.Fatalf("open learn store: %v", err)
	}
	defer learnStore.Close()

	taskArtDB := env("CORRALAI_TASK_ARTIFACTS_DB", filepath.Join(home, ".claude", "corralai_task_artifacts.sqlite3"))
	taskArtStore, err := taskartifacts.Open(taskArtDB)
	if err != nil {
		log.Fatalf("open task artifacts store: %v", err)
	}
	defer taskArtStore.Close()

	// corral certify's signed build-record ledger + its Ed25519 signing key.
	// Both must be valid together (server.go's registerBuildCert guard) or
	// report_build stays disabled rather than sign with a garbage key.
	buildDB := env("CORRALAI_BUILD_DB", filepath.Join(home, ".claude", "corralai_build.duckdb"))
	buildStore, err := buildstore.Open(buildDB)
	if err != nil {
		log.Fatalf("open build-record store: %v", err)
	}
	defer buildStore.Close()
	certifyKeyFile := env("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(home, ".claude", "corralai_certify_key"))
	certifyKey, err := buildstore.LoadOrCreateSigningKey(certifyKeyFile)
	if err != nil {
		log.Fatalf("load certify signing key: %v", err)
	}
	// certifyPub is the published half of certifyKey — the external trust
	// anchor /api/builds/{id} hands to certverify.VerifyRecord (never a key
	// derived from a stored record itself). nil (zero-value) only if
	// certifyKey somehow came back malformed, in which case the detail
	// endpoint's signature check fails closed rather than panicking.
	var certifyPub ed25519.PublicKey
	if len(certifyKey) == ed25519.PrivateKeySize {
		certifyPub, _ = certifyKey.Public().(ed25519.PublicKey)
	}

	// The transparency witness report_build anchors each signed DSSE
	// envelope to (Sigstore Rekor, by default the public instance). Anchoring
	// is an ADDITIONAL trustless guarantee on top of the signature, never a
	// build-blocking gate: if construction fails (e.g. the TUF trust root is
	// unreachable at startup), log it loudly and leave witness nil — records
	// simply come out anchored=false, exactly like a witness that goes
	// unreachable later. The brain must never crash over this.
	var certifyWitness transparency.Witness
	if len(certifyKey) == ed25519.PrivateKeySize {
		w, werr := transparency.NewRekorWitness(
			env("CORRALAI_REKOR_URL", "https://rekor.sigstore.dev"),
			transparency.WithSignerPublicKey(certifyKey.Public().(ed25519.PublicKey)),
		)
		if werr != nil {
			log.Printf("transparency witness: could not construct (build attestations will NOT be publicly witnessed, anchored=false): %v", werr)
		} else {
			certifyWitness = w
		}
	}

	browserMgr := brain.NewBrowserManager(addr)
	defer browserMgr.Close()

	// Initialize motherduck_token before any MotherDuck operations (RegisterBrain
	// and startFleetSync both need it for md: attach). Must happen first.
	initMotherDuckToken()

	// Cross-swarm coordination: brain keypair + startup registration.
	// The private key is loaded from env/file, used to register this brain's
	// public key in fleet_brains, then the env var is scrubbed. The keypair itself
	// stays in-process for future PublishIntent calls (e.g. from create_mission).
	// If the key can't be persisted (can't survive a restart without churning identity)
	// coordination is gracefully disabled — the brain continues normally.
	var crossSwarmEnabled bool
	var crossSwarmKey attest.KeyPair
	var regRetry func() bool // non-nil when startup registration needs a retry
	fleetTarget := os.Getenv("CORRALAI_MOTHERDUCK")
	fleetBrainHost, _ := os.Hostname()
	fleetBrainID := env("CORRALAI_BRAIN_ID", fleetBrainHost)
	if fleetTarget != "" {
		keyFile := env("CORRALAI_BRAIN_KEY_FILE", filepath.Join(home, ".claude", "corralai_brain_key"))
		brainKey := os.Getenv("CORRALAI_BRAIN_KEY")
		kp, keyErr := attest.LoadOrCreateKey(brainKey, keyFile)
		// Always scrub CORRALAI_BRAIN_KEY from env — private key must not linger.
		scrubSecrets([]string{"CORRALAI_BRAIN_KEY"})
		if keyErr != nil {
			log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			log.Printf("CROSS-SWARM COORDINATION DISABLED: brain key load/persist failed: %v", keyErr)
			log.Printf("fleet_claims tool will NOT be registered. Fix key persistence to enable cross-swarm dedup.")
			log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
		} else {
			// Keypair loaded — retained in-process to SIGN this brain's coordination
			// claims (from create_mission). The private key never leaves the process.
			crossSwarmKey = kp
			allowlist := parseBrainPeers(os.Getenv("CORRALAI_BRAIN_PEERS"))
			// Best-effort registration — a transient error is non-fatal (brain runs normally).
			outcome, regErr := fleet.RegisterBrain(fleetTarget, fleetBrainID, attest.PubB64(kp.Pub), allowlist, time.Now())
			if regErr != nil {
				log.Printf("fleet: brain registration error (non-fatal, coordination still enabled): %v", regErr)
				crossSwarmEnabled = true
				// Wire retry: the fleet goroutine will call this on each tick until it
				// succeeds. A brain that started while MotherDuck was briefly down will
				// register once it comes back — the comment above is now actually true.
				capturedKP, capturedAllowlist := kp, allowlist
				regRetry = func() bool {
					out, err := fleet.RegisterBrain(fleetTarget, fleetBrainID, attest.PubB64(capturedKP.Pub), capturedAllowlist, time.Now())
					if err != nil {
						log.Printf("fleet: registration retry error (non-fatal): %v", err)
						return false // keep retrying on next tick
					}
					switch out {
					case attest.Registered:
						log.Printf("fleet: brain %q registered (retry succeeded)", fleetBrainID)
					case attest.AlreadyTrusted:
						log.Printf("fleet: brain %q already trusted (retry confirmed)", fleetBrainID)
					case attest.Conflict:
						log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
						log.Printf("BRAIN IDENTITY CONFLICT for %q on registration retry — investigate before re-enabling cross-swarm.", fleetBrainID)
						log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
					case attest.Rejected:
						log.Printf("fleet: brain %q rejected on retry (allowlist mismatch — check CORRALAI_BRAIN_PEERS)", fleetBrainID)
					}
					return true // terminal: stop retrying regardless of outcome
				}
			} else {
				switch outcome {
				case attest.Registered:
					log.Printf("fleet: brain %q registered with public key (TOFU pinned)", fleetBrainID)
					crossSwarmEnabled = true
				case attest.AlreadyTrusted:
					log.Printf("fleet: brain %q already trusted (key matches pinned identity)", fleetBrainID)
					crossSwarmEnabled = true
				case attest.Conflict:
					log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
					log.Printf("BRAIN IDENTITY CONFLICT for %q — a DIFFERENT key is already pinned in fleet_brains.", fleetBrainID)
					log.Printf("This means either: (a) the key file was lost/replaced, or (b) someone is impersonating this brain.")
					log.Printf("Cross-swarm coordination DISABLED for this session. Investigate before re-enabling.")
					log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
					// crossSwarmEnabled stays false — do not expose fleet_claims under a conflicted identity.
				case attest.Rejected:
					log.Printf("fleet: brain %q not in the allowlist (CORRALAI_BRAIN_PEERS) — cross-swarm coordination disabled", fleetBrainID)
					// crossSwarmEnabled stays false.
				}
			}
		}
	} else {
		// CORRALAI_MOTHERDUCK not set: scrub CORRALAI_BRAIN_KEY anyway so it can't
		// be read by subprocesses or DuckDB's getenv if the var was set by accident.
		scrubSecrets([]string{"CORRALAI_BRAIN_KEY"})
		log.Printf("fleet: cross-swarm coordination disabled (no CORRALAI_MOTHERDUCK)")
	}

	// Fix 3: enforce invariant "key in Options ⇒ coordination enabled".
	// crossSwarmKey may be set (kp loaded) even when coordination ended up disabled
	// (Conflict, Rejected, or key-persist error). Zero it to close any future code path
	// that might accidentally act on a key that should be inert.
	if !crossSwarmEnabled {
		crossSwarmKey = attest.KeyPair{}
	}

	startFleetSync(fleet.SyncConfig{
		Coord:     dbPath,
		Mission:   missionDB,
		Queue:     queueDB,
		Telemetry: telDB,
	}, regRetry)

	// Narrator: the brain's LLM client for ask-a-bee debriefs and the fleet oracle.
	// Built once here so both consumers (fleet oracle below, ui.Deps below) share the
	// same configured client.
	narrator := llm.FromEnv()

	// learnDrafter shares the same configured LLM client, reused by the learn
	// sweep ticker below to phrase (never decide) proposal guidance. Left nil
	// when no model backend is configured — proposals still open, just without
	// a drafted guidance/skill until a human writes one.
	var learnDrafter learn.Asker
	if narrator.Available() {
		learnDrafter = narrator
	}

	// Fleet oracle: natural-language → SQL → narrated answer over the MotherDuck
	// reporting DB. Disabled when CORRALAI_MOTHERDUCK is unset or no model backend
	// is configured. motherduck_token (lowercase) was set by startFleetSync above;
	// the oracle's DuckDB session reads it for the md: attach.
	var fleetOracle *oracle.Client
	if mdTarget := os.Getenv("CORRALAI_MOTHERDUCK"); mdTarget != "" && narrator.Available() {
		fleetOracle = oracle.New(mdTarget, narrator, oracle.Options{})
		log.Printf("fleet oracle enabled (ask_fleet + UI panel)")
	} else {
		log.Printf("fleet oracle disabled (needs CORRALAI_MOTHERDUCK + a model backend)")
	}
	missionTick := time.Duration(envInt("CORRALAI_MISSION_TICK_SECONDS", 3)) * time.Second
	taskStallThreshold := time.Duration(envInt("CORRALAI_TASK_STALL_SECONDS", 300)) * time.Second
	engine := mission.NewEngine(missionStore, queueStore)
	// Engine-side finding resolutions (reflex auto-address) must reach the same
	// telemetry event log as the resolve_finding MCP tool, or model_comparison
	// reports them as forever-open.
	engine.OnFindingResolved = func(f queue.Finding, outcome string) {
		if err := telStore.Record(telemetry.Event{
			MissionID: f.MissionID, Kind: "finding_resolved", Actor: "reflex-replanner",
			Subject: f.Target, Model: f.ReporterModel,
			Detail: map[string]any{"outcome": outcome, "finding_id": f.ID, "backend": f.ReporterBackend},
		}); err != nil {
			log.Printf("telemetry finding_resolved: %v", err)
		}
	}
	// mission_completed: the engine finally speaks telemetry on its own
	// auto-complete path, mirroring the review-accept emission in
	// internal/brain/missions.go so model_comparison/mission_history never
	// have to guess whether a mission finished.
	engine.OnMissionCompleted = func(missionID int64, status string, reviewRounds int) {
		if err := telStore.Record(telemetry.Event{
			MissionID: missionID, Kind: "mission_completed", Actor: "engine",
			Detail: map[string]any{"status": status, "review_rounds": reviewRounds},
		}); err != nil {
			log.Printf("telemetry mission_completed: %v", err)
		}
	}
	engine.OnReflexCapExhausted = func(missionID int64, cap int, f queue.Finding) {
		// Telemetry only; the engine transitions the non-converging mission to the
		// terminal `failed` state itself (reliability #5) — no oscillating pause.
		log.Printf("mission %d: reflex task cap reached on finding %d (%s) — mission is not converging", missionID, f.ID, f.Type)
		if err := telStore.Record(telemetry.Event{
			MissionID: missionID, Kind: "reflex_cap_exhausted", Actor: "reflex-replanner",
			Subject: f.Target, Model: f.ReporterModel,
			Detail: map[string]any{
				"cap": cap, "finding_id": f.ID, "type": f.Type, "severity": f.Severity,
			},
		}); err != nil {
			log.Printf("telemetry reflex_cap_exhausted: %v", err)
		}
	}
	if v := os.Getenv("CORRALAI_REFLEX_MIN_SEVERITY"); v != "" {
		engine.ReflexMinSeverity = v
	}
	if n := envInt("CORRALAI_REFLEX_MAX_TASKS", 0); n > 0 {
		engine.ReflexMaxTasks = n
	}
	if v := os.Getenv("CORRALAI_CONVERGE_BLOCK_SEVERITY"); v != "" {
		engine.ConvergeBlockSeverity = v // "" (or "none") to disable the needs-review findings gate
		if v == "none" {
			engine.ConvergeBlockSeverity = ""
		}
	}

	// Repo-work mode: the brain clones, commits, and opens PRs on behalf of the
	// swarm. Token is ONLY read here — it lives in repo.Engine and is never written
	// to sandbox.MinimalEnv, a bee's env, or any log.
	repoWorkspace := env("CORRALAI_REPO_WORKSPACE", filepath.Join(os.TempDir(), "corral-repos"))
	if err := os.MkdirAll(repoWorkspace, 0o700); err != nil {
		log.Printf("repo: could not create workspace %s: %v (repo-work disabled)", repoWorkspace, err)
	}
	var repoEng *repo.Engine
	if tok := os.Getenv("CORRALAI_GIT_TOKEN"); tok != "" || os.Getenv("CORRALAI_REPO_ENABLE") == "1" {
		// Build the forge registry from environment (back-compat + multi-forge).
		// CORRALAI_GIT_TOKEN / CORRALAI_GITHUB_API are read here by ForgesFromEnv;
		// they are scrubbed after New() so no downstream reader can retrieve them.
		forges := repo.ForgesFromEnv()
		repoEng = repo.NewWithForges(tok, forges)
		engine.Repo = &repoAdapter{e: repoEng}
		engine.Workspace = repoWorkspace
		engine.Egress = egressAdapter{}
		log.Printf("repo: engine enabled (workspace %s, forges %d, github token set=%v)", // #nosec G706 -- operator-controlled config, not user input
			repoWorkspace, len(forges), tok != "")
		log.Printf("egress: gate enabled — changed files scanned for secrets (blocking) + advisory dep/license issues before push+PR")
	} else {
		log.Printf("repo: disabled (set CORRALAI_GIT_TOKEN or CORRALAI_REPO_ENABLE=1 to enable repo-work missions)")
	}
	// Security: scrub all forge token env vars now that they are held only by
	// repo.Engine. No in-process reader (DuckDB getenv, os.Environ dump, subprocess)
	// can retrieve them after this point.
	scrubSecrets([]string{"CORRALAI_GIT_TOKEN", "CORRALAI_GITHUB_API", "CORRALAI_FORGES"})

	// Repo code index: per-mission DuckDB store for hybrid BM25+semantic search.
	// Only opened when the repo engine is active (no repo engine → no working copies
	// to index). Shares the same embedder as memory/reference (one vector space).
	var repoIdx *repoindex.Store
	if repoEng != nil {
		idxDB := env("CORRALAI_REPOCODE_DB", filepath.Join(home, ".claude", "corralai_repocode.duckdb"))
		if ri, err := repoindex.Open(idxDB); err != nil {
			log.Printf("repo code index disabled: %v", err)
		} else {
			ri.SetEmbedder(embedder) // same shared vector space as memory/reference
			repoIdx = ri
			engine.Index = repoIdx
			log.Printf("repo code index enabled (%s)", idxDB)
		}
	} else {
		log.Printf("repo code index disabled (repo engine not active)")
	}

	go engine.Run(context.Background(), missionTick)
	log.Printf("missions: orchestration engine ticking every %s (reflex re-planner: >=%s, cap %d)",
		missionTick, engine.ReflexMinSeverity, engine.ReflexMaxTasks)

	// healthBook infers per-agent health (working|idle|failing) from claim/
	// complete/reclaim activity — see internal/brain/health.go (#72). Declared
	// before the reaper because the stall watchdog consults it so a failing agent
	// doesn't count as role coverage.
	healthBook := brain.NewHealthBook()

	// Reaper: requeue the tasks of bees that have gone (crashed / disconnected) so
	// the hive self-heals. Presence (coord) is authoritative — a live, heart-beating
	// bee keeps its task; the claim lease is only the fallback if presence is
	// unavailable.
	go func() {
		t := time.NewTicker(missionTick)
		defer t.Stop()
		for range t.C {
			var present map[string]bool
			active := []coord.Agent{}
			if st, err := store.CoordinationStatus(coord.PresenceWindow); err == nil && st != nil {
				active = st.ActiveAgents
				present = make(map[string]bool, len(st.ActiveAgents))
				for _, a := range st.ActiveAgents {
					present[a.Name] = true
				}
			}
			if n, err := queueStore.Reap(present); err != nil {
				log.Printf("queue: reap: %v", err)
			} else if n > 0 {
				log.Printf("queue: reaped %d stale task claim(s)", n)
			}
			// Coord-lease sibling of the queue reaper: release path leases held by
			// crashed/absent agents so a dead holder's exclusive lease can't strand
			// peers until its TTL (up to an hour).
			if reaped, err := store.ReapAbsentClaims(present); err != nil {
				log.Printf("coord: claim reap: %v", err)
			} else if len(reaped) > 0 {
				log.Printf("coord: released path leases of %d absent agent(s): %v", len(reaped), reaped)
			}
			if n, err := brain.DetectRoleStalls(queueStore, active, healthBook, taskStallThreshold, telStore); err != nil {
				log.Printf("queue: stall watchdog: %v", err)
			} else if n > 0 {
				log.Printf("queue: stall watchdog filed %d role-stall finding(s)", n)
			}
		}
	}()

	// Review-poll ticker: when the repo engine is active, periodically check all
	// open PRs that are in CHANGES_REQUESTED state and enqueue a response task for
	// any unhandled review. Runs on a separate, slower cadence than the mission Tick
	// so it doesn't amplify GitHub API calls on busy swarms.
	if repoEng != nil {
		reviewInterval := time.Duration(envInt("CORRALAI_REVIEW_POLL_SEC", 60)) * time.Second
		log.Printf("review: polling PRs for CHANGES_REQUESTED every %s", reviewInterval)
		go func() {
			t := time.NewTicker(reviewInterval)
			defer t.Stop()
			for range t.C {
				if err := engine.ReviewPoll(); err != nil {
					log.Printf("review poll: %v", err)
				}
			}
		}()
	}

	// Primary client from CORRALAI_OIDC_ISSUER/_AUDIENCE, plus any additional
	// trusted clients in CORRALAI_OIDC_CLIENTS="issuer|audience,issuer|audience"
	// (e.g. the public corral-cli user-login client alongside the corral service).
	pairs := []auth.Pair{{Issuer: issuer, Audience: audience}}
	for _, item := range strings.Split(os.Getenv("CORRALAI_OIDC_CLIENTS"), ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		kv := strings.SplitN(item, "|", 2)
		p := auth.Pair{Issuer: strings.TrimSpace(kv[0])}
		if len(kv) == 2 {
			p.Audience = strings.TrimSpace(kv[1])
		}
		pairs = append(pairs, p)
	}
	verifier, err := auth.NewVerifier(context.Background(), pairs)
	if err != nil {
		log.Fatalf("oidc verifier: %v", err)
	}
	// Subagent delegation tokens (out-of-process subagents authenticate with these).
	// Key from CORRALAI_DELEGATION_SECRET / systemd cred, else a random ephemeral key
	// (tokens then don't survive a restart — fine for short-lived subagents).
	verifier.EnableDelegation(delegationKey())

	// Role-model policy: map each role to its expected model for attribution +
	// apply-on-spawn. CORRALAI_ROLE_MODELS is model NAMEs — not a secret, not scrubbed.
	// Malformed entries are logged and skipped (non-fatal; degrade-never-block).
	roleModels, badRoleModels := rolemodel.Parse(os.Getenv("CORRALAI_ROLE_MODELS"))
	for _, bad := range badRoleModels {
		log.Printf("role-models: malformed entry (skipped): %q", bad)
	}
	if roleModels.Len() > 0 {
		log.Printf("role-models: %d role(s) configured (apply-on-spawn + topology drift enabled)", roleModels.Len())
	} else {
		log.Printf("role-models: none configured (set CORRALAI_ROLE_MODELS to enable apply-on-spawn)")
	}

	// execRing is shared: the brain's report_execution tool writes the bees' real
	// command runs into it, and the swarm UI reads it for the live execution feed.
	execRing := brain.NewExecRing()
	// activityRing is shared the same way: report_activity writes every bee tool-call
	// into it, and the swarm UI streams it so all phases show motion, not just exec.
	activityRing := brain.NewActivityRing()
	// hostBook holds each bee's runtime facts (report_host) for the UI topology view.
	hostBook := brain.NewHostBook()
	// workerSessions is the dev-mode half of the human gate: marks any MCP session
	// that identifies itself as a corral-agent worker (ClientInfo.Name or an
	// early bootstrap/report_host call), so isHumanAdmin can refuse it.
	workerSessions := brain.NewWorkerSessions()

	engine.Staffing = &mission.StaffingManager{
		Perf: &perfTracker{
			q:   queueStore,
			hb:  hostBook,
			tel: telStore,
		},
		LLM:        &llmAdapter{client: narrator},
		RoleModels: roleModels,
	}

	// The completion gate certifies gated tasks by RUNNING the verify command in a
	// jail against the brain's own working copy — never trusting a worker's self-
	// reported exit code ("a judge may not certify herself"). If no isolation
	// backend is available, fall back loudly to the recorded-execution lookup.
	// execBackend is hoisted (rather than scoped to the `if` below) because
	// the repo-gate control plane (StartGate, below) reuses this EXACT same
	// Isolator for its jail adapter — never a second one. A nil execBackend
	// means both the verify-gate AND the repo gate degrade (independent
	// verification falls back to worker-reported executions; the repo gate
	// refuses to start rather than ever run an untrusted PR check unsandboxed).
	var execBackend sandbox.Isolator
	var verifyGate brain.VerifyFunc
	if b, gerr := sandbox.Resolve(sandbox.Config{
		Backend:    os.Getenv("CORRALAI_GATE_EXEC_BACKEND"),
		UnsafeHost: os.Getenv("CORRALAI_GATE_EXEC_UNSAFE_HOST") == "1",
	}); gerr == nil {
		execBackend = b
		verifyGate = brain.NewSandboxVerify(execBackend)
		engine.Verify = verifyGate // #42: same runner re-verifies final-state at convergence
		log.Printf("verify-gate: independent verification enabled (backend %s)", execBackend.Name())
	} else {
		log.Printf("verify-gate: NO isolation backend (%v); gated completion falls back to worker-reported executions — set CORRALAI_GATE_EXEC_BACKEND", gerr)
	}

	// Repo gate (merge gate): CORRALAI_GATE_POLICIES declares which repos
	// get an independent, jailed check run against every new open-PR head.
	// Empty var => feature off (ParsePolicies returns nil, nil). Malformed
	// entries are logged and skipped — one bad entry must not take down
	// every other repo's gate (degrade-never-block).
	gatePolicies, badGatePolicies := gate.ParsePolicies(os.Getenv("CORRALAI_GATE_POLICIES"))
	for _, bad := range badGatePolicies {
		log.Printf("gate: malformed CORRALAI_GATE_POLICIES entry (skipped): %q", bad)
	}
	gateDB := env("CORRALAI_GATE_DB", filepath.Join(home, ".claude", "corralai_gate.duckdb"))

	// Control gate (v1): CORRALAI_CONTROL_GATE declares repo→owner control
	// gates; empty => feature off. Runs each owner's VETTED tests against PR
	// heads and posts corral/control-gate. Reuses execBackend for the jail.
	controlPolicies, badControl := controlgate.ParseControlPolicies(os.Getenv("CORRALAI_CONTROL_GATE"))
	for _, bad := range badControl {
		log.Printf("control-gate: malformed CORRALAI_CONTROL_GATE entry (skipped): %q", bad)
	}
	controlSpecDB := env("CORRALAI_CONTROL_GATE_SPEC_DB", filepath.Join(home, ".claude", "corralai_control_spec.duckdb"))
	controlGateDB := env("CORRALAI_CONTROL_GATE_DB", filepath.Join(home, ".claude", "corralai_control_gate.duckdb"))

	brainOpts := brain.Options{
		Coord:                 store,
		MemoryOwners:          memOwners,
		Principals:            princStore,
		Gateway:               gwStore,
		Egress:                egress,
		Artifacts:             artStore,
		Missions:              missionStore,
		Queue:                 queueStore,
		TaskArtifacts:         taskArtStore,
		Browser:               browserMgr,
		Reference:             refStore,
		Embedder:              embedder,
		Telemetry:             telStore,
		Recordings:            recStore,
		ExecRing:              execRing,
		ActivityRing:          activityRing,
		HostBook:              hostBook,
		Health:                healthBook,
		WorkerSessions:        workerSessions,
		RoleModels:            roleModels,
		TaskLeaseSeconds:      float64(envInt("CORRALAI_TASK_LEASE_SECONDS", 300)),
		ReclaimBackoffSeconds: float64(envInt("CORRALAI_RECLAIM_BACKOFF_SECONDS", 30)), // self-heal claim backoff; negative disables
		ConvergeBlockSeverity: engine.ConvergeBlockSeverity,                            // resolve_review re-checks the same gate the engine parks on
		MintToken:             verifier.MintDelegation,
		MintObserver:          verifier.MintObserver,
		Repo:                  repoEng,
		Workspace:             repoWorkspace,
		Verify:                verifyGate,
		Index:                 repoIdx,
		Oracle:                fleetOracle,
		CrossSwarm:            crossSwarmEnabled,
		CrossSwarmKey:         crossSwarmKey,
		FleetTarget:           fleetTarget,
		FleetBrainID:          fleetBrainID,
		Learn:                 learnStore,
		LearnDrafter:          learnDrafter,
		BuildStore:            buildStore,
		CertifyKey:            certifyKey,
		Witness:               certifyWitness,
		SpawnBudget: brain.SpawnBudget{
			MaxAgentsPerPrincipal: envInt("CORRALAI_MAX_AGENTS_PER_PRINCIPAL", 0),
			MaxSpawnDepth:         envInt("CORRALAI_MAX_SPAWN_DEPTH", 0),
			MaxChildrenPerParent:  envInt("CORRALAI_MAX_CHILDREN_PER_PARENT", 0),
		},
		GatePolicies:     gatePolicies,
		GateBackend:      execBackend,
		GateDB:           gateDB,
		GatePollInterval: time.Duration(envInt("CORRALAI_GATE_POLL_SECONDS", 120)) * time.Second,

		ControlPolicies:     controlPolicies,
		ControlSpecDB:       controlSpecDB,
		ControlGateDB:       controlGateDB,
		ControlPollInterval: time.Duration(envInt("CORRALAI_CONTROL_GATE_POLL_SECONDS", 120)) * time.Second,
	}
	srv := brain.NewServer(store, memStore, brainOpts)

	// Learn sweep: deterministic recurrence detection over findings + lessons,
	// feeding human-gated proposals. Degrade-never-block: any error is logged
	// loudly and the ticker keeps running — a bad sweep must never crash the brain.
	//
	// Each tick re-reads the FULL finding/lesson history (AllFindingsUnbounded
	// + LessonsForLearning) and feeds it to Sweep/Upsert as an absolute
	// snapshot of the current cluster state — not a delta. That is correct by
	// design: learn.Upsert is snapshot-based (sets count/evidence to the
	// incoming cluster rather than summing), so re-feeding unchanged history
	// is a no-op in effect, a grown cluster bumps the existing pending
	// proposal to the new size, and an approved signature's re-feed is a
	// pure no-op (see learn.Store.Upsert's doc comment).
	go func() {
		t := time.NewTicker(time.Duration(envInt("CORRALAI_LEARN_SWEEP_SECONDS", 60)) * time.Second)
		defer t.Stop()
		for range t.C {
			fs, err := queueStore.AllFindingsUnbounded()
			if err != nil {
				log.Printf("learn: findings: %v", err)
				continue
			}
			signals := make([]learn.FindingSignal, 0, len(fs))
			for _, f := range fs {
				role := ""
				if h, ok := hostBook.Get(f.Reporter); ok {
					role = h.Role
				}
				signals = append(signals, learn.FindingSignal{
					Type: f.Type, Target: f.Target, Role: role, Evidence: f.Evidence,
				})
			}
			lessons, err := memStore.LessonsForLearning(200)
			if err != nil {
				log.Printf("learn: lessons: %v", err)
				continue
			}
			docs := make([]learn.LessonDoc, 0, len(lessons))
			for _, l := range lessons {
				docs = append(docs, learn.LessonDoc{Name: l.Name, Body: l.Body, Author: l.Author})
			}
			opened, err := learnStore.Sweep(signals, docs)
			if err != nil {
				log.Printf("learn: sweep: %v", err)
				continue
			}
			for _, p := range opened {
				log.Printf("learn: proposal #%d opened (%s, %d occurrences)", p.ID, p.Signature, p.Count)
				if err := telStore.Record(telemetry.Event{
					Kind: "proposal_opened", Actor: "learn-sweep", Subject: p.Signature,
					Detail: map[string]any{"proposal_id": p.ID, "count": p.Count, "kind": p.Kind},
				}); err != nil {
					log.Printf("learn: telemetry proposal_opened: %v", err)
				}
				if learnDrafter != nil {
					pp := p
					if err := learn.Draft(context.Background(), learnDrafter, learnStore, pp); err != nil {
						log.Printf("learn: draft #%d: %v", pp.ID, err)
					}
				}
			}
		}
	}()

	// The brain listens on 127.0.0.1 behind a reverse proxy / tunnel: requests arrive
	// via localhost but carry the public Host header (your CORRALAI_ALLOWED_HOSTS),
	// which the go-sdk's localhost DNS-rebinding guard rejects with 403. That guard
	// defends a *local* MCP server against malicious web pages; here the brain is
	// proxy-fronted AND OIDC-authenticated (a browser can't forge the Bearer token),
	// so the guard only false-positives real client traffic. Disable it.
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)

	// Authorization: a valid token is necessary but not sufficient — only principals
	// in the role store may use the brain. Backed by the live store so add_member /
	// remove_principal take effect without a restart. Empty store => any authenticated.
	// Enforced ONLY when auth is enabled: in dev (no OIDC) there is no principal to
	// check, so an allowlist would just lock everyone out — skip it.
	authorizer := auth.NewAuthorizerFunc(princStore.Allowed, princStore.Count)
	authz := func(h http.Handler) http.Handler {
		if verifier.Enabled() {
			return authorizer.Middleware(h)
		}
		return h
	}
	if verifier.Enabled() {
		log.Printf("auth: OIDC enabled (%d trusted client(s)); authz: %s; memory owners: %d",
			verifier.Count(), authzDesc(authorizer.Count()), len(memOwners))
	} else {
		log.Printf("auth: DISABLED — dev mode (set CORRALAI_OIDC_ISSUER for production)")
	}

	// Host allowlist on the MCP endpoint: re-tightens the DNS-rebinding surface
	// (the SDK's localhost guard is disabled because we sit behind a reverse proxy /
	// tunnel) by only accepting the brain's real hostnames. Defaults to loopback —
	// set CORRALAI_ALLOWED_HOSTS to your public host(s) when deploying behind a proxy.
	allowedHosts := splitList(os.Getenv("CORRALAI_ALLOWED_HOSTS"))
	if len(allowedHosts) == 0 {
		allowedHosts = []string{"127.0.0.1", "localhost"}
	}

	// In-process abuse protection (edge-independent — works without Cloudflare):
	// per-IP limit on all routes, per-principal limit on authenticated MCP calls,
	// and a request-body cap. ipHeader is honored only behind a trusted proxy.
	ipHeader := os.Getenv("CORRALAI_CLIENT_IP_HEADER") // e.g. CF-Connecting-IP; empty => RemoteAddr
	ipLim := limit.New(envInt("CORRALAI_RATELIMIT_IP_PER_MIN", 300), envInt("CORRALAI_RATELIMIT_IP_BURST", 100))
	userLim := limit.New(envInt("CORRALAI_RATELIMIT_USER_PER_MIN", 600), envInt("CORRALAI_RATELIMIT_USER_BURST", 200))
	maxBody := int64(envInt("CORRALAI_MAX_BODY_BYTES", 1<<20))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// /api/certify/pubkey: publish the certify Ed25519 public key so a third
	// party holding only a persisted build_records row (no brain
	// credentials) can independently verify it with certify.VerifyDSSE.
	// Unauthenticated like /healthz — that's the whole point. Only mounted
	// when certifyKey is a valid, fully-loaded Ed25519 private key (same
	// guard registerBuildCert uses); an invalid/missing key means the brain
	// never signs statements, so there is no key worth publishing.
	if len(certifyKey) == ed25519.PrivateKeySize {
		mux.HandleFunc("/api/certify/pubkey", brain.CertifyPubkeyHandler(certifyKey.Public().(ed25519.PublicKey)))
	}
	// A read-only observer token may view the swarm but never act: /mcp (the whole
	// tool surface) is an action surface, so reject it outright.
	denyReadOnly := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.ReadOnly(r) {
				http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	// /mcp: body cap → authenticate (sets TokenInfo) → per-principal limit → deny-RO → authorize → MCP.
	mcpChain := limit.MaxBody(maxBody, verifier.Wrap(userLim.ByKey(auth.PrincipalKey, denyReadOnly(authz(mcpHandler)))))
	mux.Handle("/mcp", mcpChain)
	mux.Handle("/mcp/", mcpChain)
	// Live swarm UI + state stream. CLI-only: the UI endpoints are BEARER-gated with
	// the same token + allowlist as /mcp, so the only way in is the `corral-observe`
	// proxy (which carries the operator's or an observer token). A direct browser hit
	// without a bearer gets 401. No browser-login flow lives in the brain. When auth
	// is disabled (dev) the wraps are no-ops and the UI is open on localhost.
	// Proposal fan-out from the UI's approve button reuses the exact same
	// logic the approve_proposal MCP tool runs (brain.ApproveProposal) — the
	// operator clicking "approve" in the browser and an MCP client calling
	// approve_proposal are indistinguishable at the fan-out level. Reject is
	// the symmetric thin wrapper over brain.RejectProposal.
	proposalPromote := func(id int64, actor string) error {
		_, err := brain.ApproveProposal(learnStore, memStore, artStore, telStore, id, actor, false, false)
		return err
	}
	proposalReject := func(id int64, reason string) error {
		return brain.RejectProposal(learnStore, id, reason)
	}
	historyList := func() ([]brain.MissionSummary, error) {
		return brain.MissionHistoryList(missionStore, queueStore, telStore, learnStore)
	}
	historyDetail := func(id int64) (*brain.MissionDetail, error) {
		return brain.MissionHistoryDetail(missionStore, queueStore, telStore, learnStore, id)
	}
	replayStream := func(missionID int64) ([]brain.ReplayEvent, error) {
		return brain.BuildReplayStream(queueStore, telStore, missionID)
	}
	uiHandler := verifier.Wrap(authz(ui.Handler(ui.Deps{Coord: store, Mem: memStore, Gateway: gwStore, Bus: bus, MemOwners: memOwners, Roles: princStore, Queue: queueStore, Missions: missionStore, Executions: execRing, Activity: activityRing, Hosts: hostBook, Health: healthBook, Narrator: narrator, Telemetry: telStore, Oracle: fleetOracle, RoleModels: roleModels, Staffing: engine.Staffing, Learn: learnStore, Promote: proposalPromote, Reject: proposalReject, History: historyList, HistoryDetail: historyDetail, Replay: replayStream, Artifacts: artStore, TaskArtifacts: taskArtStore, BuildStore: buildStore, CertifyPub: certifyPub, Witness: certifyWitness, Version: version})))
	if verifier.Enabled() {
		log.Printf("ui: bearer-gated (view via `corral-observe`)")
	} else {
		log.Printf("ui: OPEN — dev (no auth)")
	}
	mux.Handle("/", uiHandler)

	// Repo gate (merge gate): starts the poller (if configured + a jail
	// backend is available) and, only then, wires the read endpoint —
	// GET /api/gate/run — behind the EXACT SAME auth wrapper (verifier.Wrap
	// + authz) every other /api/* route uses, so a stored gate run is no
	// more exposed than /api/state is. StartGate itself decides whether the
	// feature is on; a nil store here just means "off" (or "disabled for
	// want of a jail backend") — nothing more to wire.
	if gateStore, gerr := brain.StartGate(context.Background(), brainOpts); gerr != nil {
		log.Fatalf("gate: %v", gerr)
	} else if gateStore != nil {
		mux.Handle("/api/gate/run", verifier.Wrap(authz(brain.GateRunHandler(gateStore))))
	}

	// Control gate: same StartX pattern as the repo gate above, but v1 has
	// no read endpoint yet (Task 6 wires `corral control` CLI dispatch).
	// Degrade-never-block: log and continue rather than failing brain startup.
	if _, _, cerr := brain.StartControlGate(context.Background(), brainOpts); cerr != nil {
		log.Printf("control-gate: %v", cerr)
	}

	// Outermost on every route: Host allowlist + per-IP rate limit.
	root := hostAllow(allowedHosts, ipLim.ByIP(ipHeader, mux))

	// Hardened server: header/read/idle timeouts (anti-slowloris). No WriteTimeout
	// — SSE streams (/events and MCP responses) are intentionally long-lived.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("corral brain listening on %s  (MCP: /mcp/, health: /healthz, db: %s)", addr, dbPath)
	if err := listen(httpSrv); err != nil {
		log.Fatal(err)
	}
}

// repoAdapter wraps *repo.Engine and satisfies mission.RepoOps. The two
// packages use identical field sets (ReviewInfo/ReviewCommentInfo mirror
// repo.Review/repo.ReviewComment) but are separate types so neither package
// imports the other. This adapter is the single conversion point.
//
// After the multi-forge refactor, REST methods take repoURL (not owner/repo):
// the Engine resolves the forge Provider from the URL's host internally.
type repoAdapter struct{ e *repo.Engine }

func (a *repoAdapter) Commit(ctx context.Context, dir, msg string) (bool, error) {
	return a.e.Commit(ctx, dir, msg)
}
func (a *repoAdapter) Push(ctx context.Context, dir, branch string) error {
	return a.e.Push(ctx, dir, branch)
}
func (a *repoAdapter) OpenPR(ctx context.Context, repoURL, head, base, title, body string) (string, error) {
	return a.e.OpenPR(ctx, repoURL, head, base, title, body)
}
func (a *repoAdapter) ChangedFiles(ctx context.Context, dir string) ([]string, error) {
	return a.e.ChangedFiles(ctx, dir)
}
func (a *repoAdapter) ChangedFilesRange(ctx context.Context, dir, base string) ([]string, error) {
	return a.e.ChangedFilesRange(ctx, dir, base)
}

// egressAdapter wires internal/egress into mission.EgressScanner — the same
// single-conversion-point pattern as repoAdapter, keeping the mission package
// from importing internal/egress directly.
type egressAdapter struct{}

func (egressAdapter) Scan(ctx context.Context, dir string, files []string) []mission.EgressFinding {
	findings := egress.Scan(ctx, dir, files)
	out := make([]mission.EgressFinding, len(findings))
	for i, f := range findings {
		out[i] = mission.EgressFinding{Path: f.Path, Line: f.Line, Rule: f.Rule, Sample: f.Sample, Severity: f.Severity}
	}
	return out
}
func (a *repoAdapter) ListReviews(ctx context.Context, repoURL string, pr int, etag string) ([]mission.ReviewInfo, string, bool, error) {
	revs, newEtag, notMod, err := a.e.ListReviews(ctx, repoURL, pr, etag)
	if err != nil || notMod {
		return nil, newEtag, notMod, err
	}
	out := make([]mission.ReviewInfo, len(revs))
	for i, rv := range revs {
		out[i] = mission.ReviewInfo{ID: rv.ID, State: rv.State, Body: rv.Body, SubmittedAt: rv.SubmittedAt, User: rv.User}
	}
	return out, newEtag, false, nil
}
func (a *repoAdapter) ListReviewComments(ctx context.Context, repoURL string, pr int) ([]mission.ReviewCommentInfo, error) {
	cs, err := a.e.ListReviewComments(ctx, repoURL, pr)
	if err != nil {
		return nil, err
	}
	out := make([]mission.ReviewCommentInfo, len(cs))
	for i, c := range cs {
		out[i] = mission.ReviewCommentInfo{Path: c.Path, Line: c.Line, Body: c.Body, User: c.User}
	}
	return out, nil
}
func (a *repoAdapter) GetPR(ctx context.Context, repoURL string, pr int) (string, bool, error) {
	return a.e.GetPR(ctx, repoURL, pr)
}
func (a *repoAdapter) PostComment(ctx context.Context, repoURL string, pr int, body string) error {
	return a.e.PostComment(ctx, repoURL, pr, body)
}
func (a *repoAdapter) AuthLogin(ctx context.Context, repoURL string) (string, error) {
	return a.e.AuthLogin(ctx, repoURL)
}

type perfTracker struct {
	q   *queue.Store
	hb  *brain.HostBook
	tel *telemetry.Store
}

func (p *perfTracker) GetRoleModelStats() []mission.ModelStats {
	lb, err := brain.BuildLeaderboard(p.q, p.hb, p.tel)
	if err != nil {
		return nil
	}
	var stats []mission.ModelStats
	for _, cell := range lb.Cells {
		stats = append(stats, mission.ModelStats{
			Model:           cell.Model,
			Role:            cell.Role,
			TasksCompleted:  cell.TasksCompleted,
			AvgTaskDuration: cell.AvgTaskDuration,
			ExecPassRatePct: cell.ExecPassRatePct,
		})
	}
	return stats
}

type llmAdapter struct {
	client *llm.Client
}

func (a *llmAdapter) Generate(ctx context.Context, system, prompt string) (string, error) {
	return a.client.Ask(ctx, system, prompt)
}

func (a *llmAdapter) Available() bool {
	return a.client.Available()
}
