// SPDX-License-Identifier: Elastic-2.0

// Command corral-wrangler is the coordination daemon — the brain — as its
// own binary: an OIDC-authenticated, MCP-native server that thin clients
// (coding agents on any machine, corral-agent workers, the consoles)
// connect to over streamable-HTTP. The wrangler is the hand who manages the
// string of horses; this is the coordinator for the agents that share a
// codebase. Nothing here is required for a `corral certify` verdict, and
// nothing here is read by one — see docs/corral/brain.md.
//
//	corral-wrangler   # serves /mcp/ + /healthz on $CORRALAI_ADDR
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
//	CORRALAI_ALLOW_INSECURE    set "1" to allow auth-disabled startup on a non-loopback CORRALAI_ADDR (refused otherwise, H-3)
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
//	                          — owner= MUST equal the control owner's authenticated principal (the identity they
//	                          author controls under), else the gate finds no vetted controls
//	                           — runs the owner's VETTED tests against PR heads, posts corral/control-gate
//	CORRALAI_CONTROL_GATE_SPEC_DB  control-gate vetted-tests store (default ~/.claude/corralai_control_spec.duckdb)
//	CORRALAI_CONTROL_GATE_DB       control-gate dedupe/index store (default ~/.claude/corralai_control_gate.duckdb)
//	CORRALAI_CONTROL_GATE_POLL_SECONDS  how often the control gate polls for new PR heads (default 120)
//	CORRALAI_BUGCATCH_DB       adversarial pool's bug-catching scorecard store DuckDB path
//	                           (default ~/.claude/corralai_bugcatch.duckdb); also read by `corral scorecard`
//	CORRALAI_CRITICSCORE_DB    adversarial pool's critic-accuracy store DuckDB path (default
//	                           ~/.claude/corralai_criticscore.duckdb); the scorecard's C-PREC column and
//	                           `corral criticscore` read it over the API — see CORRAL_BRAIN below
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/pdbethke/corralai/internal/wranglerd"
)

// stampedVersion is what -ldflags "-X main.stampedVersion=..." writes; the
// same convention as cmd/corral, so one build line stamps both binaries.
var stampedVersion = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			fmt.Print(usage)
			return
		case "-version", "--version", "version":
			fmt.Println("corral-wrangler", resolveVersion(stampedVersion))
			return
		case "register", "heartbeat", "claim", "release", "done", "who", "list":
			os.Exit(runBroker(os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
		case "serve":
			wranglerd.Run(resolveVersion(stampedVersion))
			return
		default:
			fmt.Fprintf(os.Stderr, "corral-wrangler: unknown command %q\n%s", os.Args[1], usage)
			os.Exit(2)
		}
	}
	wranglerd.Run(resolveVersion(stampedVersion))
}

const usage = `corral-wrangler — the coordinator for agents that share a codebase.

The broker, no server needed — the verbs open the daemon's own store as a
local file ($CORRALAI_DB, default ~/.claude/corralai_coord.sqlite3), so a
claim from the shell and a claim over MCP are the same claim; the OS user
is the principal:

  corral-wrangler register  --as <session> --task "<what you are doing>"   register / refresh, see who else is here
  corral-wrangler heartbeat --as <session> [--task "<handoff note>"]        keep the session and its claims alive
  corral-wrangler claim     --as <session> [--reason why] [--ttl 2h] PATH…   claim paths before editing; exit 1 if any is held
  corral-wrangler release   --as <session> [PATH…]                          release these (no PATH = all)
  corral-wrangler done      --as <session> --task "<summary>" [PATH…]       leave the record of what was finished, release all
  corral-wrangler who       [--as <session> | <session>]                    a session and what it holds
  corral-wrangler list                                                       every active session, live claim, recent completion
  (add --json to any verb for the record as data)

The daemon (MCP coordination, gates, memory, console) — the same store, served:

  corral-wrangler [serve]    start the server on $CORRALAI_ADDR (default 127.0.0.1:9019)
  corral-wrangler --version  print the version
  corral-wrangler --help     this text

Configuration is by environment; every variable is documented in this
binary's source header (cmd/corral-wrangler/main.go) and on the generated
CLI reference page. Nothing here is required for a corral certify verdict,
and nothing here is read by one — see docs/corral/brain.md.
`

func resolveVersion(stamped string) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi != nil {
		switch v := bi.Main.Version; v {
		case "", "(devel)":
		default:
			return v
		}
	}
	return "dev"
}
