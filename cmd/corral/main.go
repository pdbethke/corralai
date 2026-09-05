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
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/eval"
	"github.com/pdbethke/corralai/internal/wranglerd"
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
	// A HAND-MAINTAINED ALLOWLIST, and the failure mode is quiet: a name
	// missing here is not an error, it falls through to booting the
	// coordination server. `corral ui -h` printed the brain's usage until "ui"
	// was added. Keep it in step with the switch below — TestEverySubcommandIsDispatchable.
	case "certify", "secret", "control", "scorecard", "criticscore", "matrix", "models", "scans", "seal", "ui", "eval", "mcp", "doctor", "demo", "verify":
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

// printVersion writes the version banner to OUT, not to stderr. It used to go
// through log.Println (stderr), so `corral version | grep`, `$(corral version)`
// and any CI step capturing a version got an empty string while the text still
// scrolled past on the terminal looking correct — a failure invisible exactly
// where someone would look for it. stderr is for diagnostics; the answer to a
// question the user asked is stdout, which is what `corral -h` already does.
//
// errOut is taken (and left unused on the success path) so the signature says
// plainly that nothing about a successful version query belongs on stderr.
func printVersion(out, errOut io.Writer) {
	_ = errOut
	fmt.Fprintln(out, "corral", version)
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
  corral certify [<ref>] [--out <file>] [--net=false] [--produced-by a,b] -- <check-cmd>...
                                  certify a change by execution: check out <ref> (default
                                  HEAD) into a jail, run <check-cmd> there, and write a
                                  signed, offline-verifiable record; exits with
                                  <check-cmd>'s own exit code
                                  signs locally (no server) unless --brain is given
                                  flags: --produced-by a,b   --out <file>   --net=false
                                         --repo/--commit/--branch (default: read via git)
  corral certify --brain <url> [flags] -- <check-cmd>...
                                  same as above, and also post the signed record to a
                                  brain (report_build) as a tamper-evident build attestation
  corral certify --adversarial --code <path> --goal "<text>" [--test <path>] -- <test cmd>
                                  grade a change's own tests: fire the adversarial pool on the
                                  brain, poll to a signed verdict
  corral certify --repo <dir> [--top n|--all] [--goals <file>] [--dry-run] [--swarm n] [-- <test cmd>]
                                  fan the --local audit out over a WHOLE repository: enumerate
                                  every source file with a paired test, rank them by churn x size,
                                  audit the top --top (default 25, --all for every one) through a
                                  bounded swarm, and print a repo report whose kill rate is over
                                  the AUDITED surface only, with every excluded file accounted for
                                  by reason — including the ones the bound left out
                                  each file's goal is DERIVED from its source by --derive-model;
                                  --goals <file> instead takes goals from a JSON map and makes no
                                  model call
                                  --dry-run stops after enumeration (no jail, no LLM calls)
                                  an explicit -- <test cmd> grades EVERY file, so it is refused
                                  when the scan spans more than one language (omit it and each
                                  file is graded with its own language's stock command)
                                  the report is NOT signed yet — that lands with the sealed
                                  repo statement
  corral certify verify <record-file> [--pubkey <hex>|--brain <url>] [--allow-unanchored]
                                  independently verify a --out (or report_build) record: the
                                  Ed25519 signature, the ledger's hash chain, and that the
                                  statement is bound to that exact ledger head — requires a
                                  trusted key via --pubkey or --brain (a record's own
                                  embedded public_key is never a trust anchor); prints
                                  "verified" and exits 0, or names the failing check on
                                  stderr and exits non-zero
  corral certify pubkey           print the local signing pubkey (for --pubkey trust anchors)
  corral scorecard [--json]       show the bug-catching scorecard (recall/precision per model×role,
                                  plus a C-PREC column: the test-critic role's execution-checked
                                  precision from criticscore adjudications);
                                  table by default, or the raw cells as indented JSON with --json
  corral models rank [flags]      rank the models that have sat in each seat by corral's OWN recorded
                                  evidence — a DIFFERENT metric per seat: the writer by proven gaps
                                  per survivor attempted, the generator by valid mutants the dev
                                  suite missed per run, the critic by precision against human
                                  adjudication; the goal-deriver is reported as not scored rather
                                  than given an invented number. A model below --min-runs (default 5)
                                  is printed with its real numbers, marked insufficient, and never
                                  preferred. DISCLOSURE, NOT SELECTION: it writes no config, changes
                                  no default and staffs no seat — corral has no default models.
                                  flags: --db <dsn> (a pushed warehouse instead of the local
                                  bug-catching ledger; unreachable REFUSES, never falls back)
                                  --seat <role>  --lang <name>  --min-runs n  --json
  corral criticscore list         list execution-checked test-critic findings still awaiting human
                                  adjudication — the local store certify --local writes, or a
                                  running brain's when CORRAL_BRAIN is set
  corral criticscore show <id>    print one finding in full (model, target test, evidence)
  corral criticscore confirm <id> record a human "confirmed" verdict — the finding was real
  corral criticscore refute <id>  record a human "refuted" verdict — the finding was wrong
                                  (confirm/refute permanently override the pool's own auto-adjudication;
                                  this IS the human gate the critic-precision column measures)
  corral matrix list [--json]     show the tests×mutants matrix (swarm slice 5): per-test
                                  execution-proven adequacy against a run's own mutant set, plus a
                                  safe-to-delete candidate list — populated only by runs opted in via
                                  certify --local --matrix (requires CORRAL_BRAIN — no offline mode)
  corral scans list|show [flags]  read the scan ledger certify --repo --record writes: list
                                  shows recent scans, show <id> their per-file dispositions —
                                  including WHY a proven-gap count of 0 is 0 (writer failed / test
                                  unsound / tried and missed), which the bare number cannot say.
                                  show <id> --evidence prints the pool's own authored test, kept
                                  even when it proved nothing — that is the case worth reading.
                                  Local DuckDB file, no brain required:
                                  --db <path> (default $CORRALAI_SCANS_DB, else
                                  ~/.claude/corralai_scans.duckdb), --limit n, --json
  corral verify --ledger <dir>    walk a ledger directory's chain: every entry's hash against its
                                  bytes, every link against its predecessor, every signature
                                  against --pub or the local certify key; one line per entry,
                                  an edited or removed entry named; unsigned said, never "verified"
  corral ledger append <entry> <dir>
                                  re-link an entry to <dir>'s current head (re-hash, re-sign, place)
                                  — the verb a fetch → append → push loop runs, since a chain is
                                  one writer at a time and a git rebase moves the commit, not the link
  corral ledger verify <dir>      the same walk as corral verify --ledger
  corral verify --attest <path> [flags]
                                  the checker for a certify --repo --attest statement: verifies
                                  its DSSE signature (against --pub or the local certify key,
                                  reporting who signed either way), and — opted in per flag —
                                  recomputes the pushed warehouse rows' hash from a --db and
                                  confirms a Rekor entry (--rekor-index, or read from --db)
                                  matches the envelope on disk. Prints check marks and one
                                  plain sentence per check; exits 1 only on a real mismatch.
                                  Different from "corral certify verify", which checks a
                                  corral certify BUILD record, a different artifact.
                                  flags: --db <dsn>  --rekor-index <n>  --pub <hex>
  corral ui [flags]              browse that same seal in a browser: a local, read-only page over
                                  the ledger, loopback by default. No brain, no writes — if
                                  corral seal can answer it, this shows it.
  corral seal [flags]            the repo's CURRENT state as the union of still-valid verdicts,
                                  read from a certify --repo --push warehouse (many audits, one
                                  current state — not one scan's snapshot). Reads corral_seal
                                  (latest kill-rate-bearing row per path), creating the view if
                                  a writer never has. With --repo <dir>: judges each of the
                                  repo's churn x size top-N ("hot") files live (bytes unchanged
                                  since the audit), stale (changed since), never audited,
                                  unreadable, or unknown (the row recorded no validity key) —
                                  and prints "coverage: N of M hot files carry a live verdict",
                                  which counts the live ones only.
                                  Without --repo: the warehouse's latest verdict per path, no
                                  live/stale judgement. Read-only — never writes a row.
                                  flags: --db <dsn> (default $CORRALAI_SCANS_DB, else
                                  ~/.claude/corralai_scans.duckdb) --repo <dir> --top n (default 20)
                                  --json
  corral demo [flags]             a complete audit of a tiny project, in ONE command: writes a
                                  small Go package with a five-clause password rule and a test
                                  that checks only two of them, then audits it with the real
                                  certify --local. Needs a Go toolchain (you installed corral
                                  with one) and a provider key — no venv, no database, no
                                  fixtures. The fastest honest answer to "what does this do?"
                                  flags: --writer-model/--mutant-model (required; corral has no
                                         default models) --critic-model --dir
  corral mcp                     serve corral's findings corpus over stdio MCP, so an editor
                                  or agent can read what past audits found. READ-ONLY and
                                  local: no brain, no writes, no network listener.
  corral doctor [flags] [-- <test cmd>]
                                  check the environment BEFORE paying for a run: does the
                                  sandbox start, is your test command's toolchain reachable
                                  INSIDE it, has every grading seat been given a model (corral
                                  has no defaults) with a credential for it, and does the file
                                  you named have a test corral can pair with. Every check is free — no model is ever called — and
                                  they run in the order an audit would hit them, so the first
                                  FAIL is the first thing to fix. Exits non-zero if any failed.
                                  flags: --code <path> --test <path> (adds the pairing check)
                                         --jail <backend> (default: auto-detect)
                                         --mutant-model/--writer-model/--critic-model <name>
                                  It does NOT check two things that need a real seeded
                                  workspace: whether your suite passes on UNMUTATED code inside
                                  the sandbox (the most common way an audit dies), and whether a
                                  multi-file project needs --repo-dir.
  corral eval [flags]             run the adversarial pool across the versioned eval corpus and
                                  print a soundness report (does the recall metric catch known gaps?)
                                  flags: --corpus <path> (default eval/corpus/manifest.json)
                                         --iterations <n> (default 1)   --only <id,id,...>
                                         --brain <url> (or $CORRAL_BRAIN)
                                         --progress <path> (default eval/.eval-progress.json)
  corral --version                print the build version and exit
  corral -h                       print this help and exit

Configuration is entirely environment variables — see CORRALAI_ADDR,
CORRALAI_DB, and the rest of the // Env: block at the top of this binary's
main.go (also reproduced in the generated CLI reference).
`
}

// version is set at build time via -ldflags "-X main.version=...", and falls
// back to the module version Go embeds in the binary — see resolveVersion.
var version = resolveVersion(stampedVersion, debug.ReadBuildInfo)

// stampedVersion is what -ldflags "-X main.stampedVersion=..." writes. It stays
// "dev" for any build that does not pass it, which includes the one that
// matters most: `go install <module>/cmd/corral@latest`, the install line in
// the README and the one every first-time reader uses. Before this, every such
// user's `corral version` said "dev" and no bug report could name a build.
var stampedVersion = "dev"

// resolveVersion prefers an explicitly stamped version (a release build knows
// more than the module graph, and may be building from a checkout rather than
// a tagged module), then the module version Go records in the binary for a
// `go install <module>@<version>`.
//
// "(devel)" — what a local `go build` reports — is NOT a version and must never
// be printed as one; it, an empty string, and unavailable build info all fall
// back to "dev", which is exactly today's behaviour for a developer in-tree.
func resolveVersion(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	bi, ok := readBuildInfo()
	if !ok || bi == nil {
		return "dev"
	}
	switch v := bi.Main.Version; v {
	case "", "(devel)":
		return "dev"
	default:
		return v
	}
}

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
		os.Exit(runCertify(os.Args[2:], realRunner{}, mcpPoster{}, realJail{}, loadLocalCertifyKey, os.Stdout, os.Stderr))
	case "control":
		if err := runControl(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "corral control:", err)
			os.Exit(1)
		}
		return
	case "scorecard":
		// -h MUST NOT OPEN A STORE. Both stores below are created lazily by
		// the runs that write them, so a new operator has neither — and
		// `corral scorecard -h` answered them with a DuckDB IO error instead
		// of usage. Asking a command what it does is the one thing that can
		// never require state to already exist.
		//
		// Caught by CI: gen-cli-docs.sh captures each subcommand's real -h,
		// and on a fresh runner the captured "help" was that error. The
		// generated reference is host-dependent exactly when help is.
		if wantsHelp(os.Args[2:]) {
			os.Exit(runScorecard(os.Args[2:], nil, os.Stdout))
		}
		// The bugcatch DuckDB file is single-process: corral.service (the
		// running brain) already holds it read-write, and DuckDB refuses a
		// second concurrent open — even read-only (verified: PID conflict
		// error, not silent success). So whenever a brain is configured,
		// read the scorecard over HTTP from its /api/bugcatch instead of
		// touching the file; only fall back to the local DuckDB file when
		// no brain is configured (the offline/standalone case).
		if brainURL := strings.TrimSpace(os.Getenv("CORRAL_BRAIN")); brainURL != "" {
			token, err := brainToken()
			if err != nil {
				fmt.Fprintln(os.Stderr, "corral scorecard:", err)
				os.Exit(1)
			}
			os.Exit(runScorecard(os.Args[2:], newHTTPScorecardReader(brainURL, token), os.Stdout))
		}
		home, _ := os.UserHomeDir()
		bugCatchDB := env("CORRALAI_BUGCATCH_DB", filepath.Join(home, ".claude", "corralai_bugcatch.duckdb"))
		bugCatchStore, err := bugcatch.Open(bugCatchDB)
		if err != nil {
			if strings.Contains(err.Error(), "Conflicting lock is held") {
				fmt.Fprintln(os.Stderr, "corral scorecard: the bugcatch DB is held by the running brain — set CORRAL_BRAIN (and CORRALAI_BRAIN_TOKEN via `corral secret`) to read it over the API, or stop the brain first")
			} else {
				fmt.Fprintln(os.Stderr, "corral scorecard: open bugcatch store:", err)
			}
			os.Exit(1)
		}
		defer bugCatchStore.Close()
		// Best-effort critic join: a nil store just leaves C-PREC empty, which
		// is the pre-existing behavior. Refusing to print a scorecard because
		// the critic file is held elsewhere would trade the whole table for
		// one column.
		var localCritic *criticscore.Store
		if cs, cerr := criticscore.Open(localCriticScoreDBPath()); cerr == nil {
			localCritic = cs
			defer func() { _ = cs.Close() }()
		}
		os.Exit(runScorecard(os.Args[2:], localScorecardReader{store: bugCatchStore, critic: localCritic}, os.Stdout))
	case "models":
		// Read-only disclosure over the stores certify already writes. It
		// opens nothing writable, changes no default, and cannot influence a
		// run — see models_rank.go.
		os.Exit(runModels(os.Args[2:], ".", defaultRankLoader, os.Stdout, os.Stderr))
	case "demo":
		// The two-minute first run: a self-contained project plus the REAL
		// certify --local, so nothing about the environment can spoil a
		// newcomer's first impression of what the tool does.
		os.Exit(runDemo(os.Args[2:], os.Stdout, os.Stderr))
	case "doctor":
		// Free, fast, and BEFORE any spend: see doctor.go for why the checks
		// are ordered the way the audit itself would hit them.
		os.Exit(runDoctor(os.Args[2:], os.Stdout, os.Stderr))
	case "mcp":
		// -h HANGS WITHOUT THIS. The server reads stdio forever, so `corral mcp
		// -h` never returned — the same failure the CLI reference generator was
		// built to catch ("a docs generator cannot capture text a binary
		// refuses to print"), which is why this subcommand had no reference
		// section and no manifest rows until now. Handled before the store is
		// opened, for the same reason as scorecard and criticscore above.
		if wantsHelp(os.Args[2:]) {
			fmt.Print(mcpUsage)
			os.Exit(0)
		}
		// Serve the findings to a coding agent over stdio, read-only, from the
		// same local store `certify --local` writes. See mcp_findings.go for
		// why adjudication is deliberately absent from that surface.
		cs, err := criticscore.Open(localCriticScoreDBPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "corral mcp: opening the local findings store: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = cs.Close() }()
		os.Exit(runFindingsMCP(context.Background(), cs, os.Stderr))
	case "criticscore":
		// -h must not open a store; see the scorecard case above.
		if wantsHelp(os.Args[2:]) {
			os.Exit(runCriticScore(os.Args[2:], nil, nil, os.Stdout, os.Stderr))
		}
		// With CORRAL_BRAIN set, show/confirm/refute go through the brain's
		// ADMIN-gated MCP tools, which need the caller's bearer identity for
		// the isHumanAdmin check and the audit trail. Without one, they run
		// against the local store `certify --local` writes — see
		// localCriticScore for why a single-operator DuckDB file under $HOME
		// does not need that gate to mean something.
		brainURL := strings.TrimSpace(os.Getenv("CORRAL_BRAIN"))
		if brainURL == "" {
			// No brain: fall back to the LOCAL store that `certify --local`
			// now writes. Without this the findings corpus was unreachable for
			// everyone who is not running a daemon — which is everyone the
			// quickstart talks to — and scorecard's C-PREC column could never
			// be filled by them. See localCriticScore's doc comment for why
			// this does not weaken the brain's admin gate.
			cs, err := criticscore.Open(localCriticScoreDBPath())
			if err != nil {
				fmt.Fprintf(os.Stderr, "corral criticscore: opening the local critic store: %v\n", err)
				fmt.Fprintln(os.Stderr, "corral criticscore: (or set CORRAL_BRAIN to adjudicate against a running brain instead)")
				os.Exit(1)
			}
			defer func() { _ = cs.Close() }()
			local := localCriticScore{store: cs}
			os.Exit(runCriticScore(os.Args[2:], local, local, os.Stdout, os.Stderr))
		}
		token, err := brainToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, "corral criticscore:", err)
			os.Exit(1)
		}
		os.Exit(runCriticScore(os.Args[2:], newHTTPCriticScoreLister(brainURL, token), mcpCriticScoreAdmin{brainURL: brainURL}, os.Stdout, os.Stderr))
	case "matrix":
		// Same reasoning as criticscore above: the matrix store is a
		// single-process DuckDB file the running brain already holds
		// read-write, and matrix data only exists at all from a brain-run
		// (or a --local run's own signed ledger, which this command does
		// not read) — no offline mode.
		brainURL := strings.TrimSpace(os.Getenv("CORRAL_BRAIN"))
		if brainURL == "" {
			fmt.Fprintln(os.Stderr, "corral matrix: set CORRAL_BRAIN (and CORRALAI_BRAIN_TOKEN via `corral secret`) — matrix has no offline mode")
			os.Exit(1)
		}
		token, err := brainToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, "corral matrix:", err)
			os.Exit(1)
		}
		os.Exit(runMatrix(os.Args[2:], newHTTPMatrixReader(brainURL, token), os.Stdout, os.Stderr))
	case "scans":
		// Unlike criticscore/matrix above, this needs NO brain: the scan
		// ledger is a local DuckDB file `certify --repo --record` writes on
		// this same machine, so the read side is deliberately offline too.
		// DuckDB's single-writer lock still applies — openScanStore says so
		// when a concurrent scan holds the file.
		os.Exit(runScans(os.Args[2:], openScanStore, os.Stdout, os.Stderr))
	case "ui":
		// The same reader `seal` uses, and nothing else — see runUI on why this
		// is not internal/ui. openSealDB only ever SELECTs, so `corral ui -h`
		// needs no store: the help guard above applies here for the same reason
		// it applies to scorecard and criticscore.
		if wantsHelp(os.Args[2:]) {
			os.Exit(runUI(os.Args[2:], func(string) (sealReader, error) { return nil, nil }, os.Stdout, os.Stderr))
		}
		os.Exit(runUI(os.Args[2:], openSealDB, os.Stdout, os.Stderr))
	case "seal":
		// Also fully offline: a `--push` warehouse is any DuckDB the
		// operator owns, and this reader only ever SELECTs from it (and, on
		// a first read, creates corral_seal if a writer never has).
		os.Exit(runSeal(os.Args[2:], openSealDB, os.Stdout, os.Stderr))
	case "eval":
		os.Exit(runEval(os.Args[2:], func(brainURL, corpusVersion string) eval.PoolRunner {
			return mcpPoolRunner{client: mcpAdvClient{}, brainURL: brainURL, corpusVersion: corpusVersion,
				poll: 5 * time.Second, timeout: 15 * time.Minute}
		}, os.Stdout, os.Stderr))
	case "ledger":
		os.Exit(runLedger(os.Args[2:], os.Stdout, os.Stderr))
	case "verify":
		// `corral verify` — the checker for a `certify --repo --attest`
		// AUDIT statement (signature + --db warehouse rows + Rekor
		// inclusion). Distinct from `corral certify verify`, which checks
		// a `corral certify` BUILD record — see verify_attest.go's doc.
		os.Exit(runVerifyAttest(os.Args[2:], os.Stdout, os.Stderr))
	}
	if showVersion(os.Args[1:]) {
		printVersion(os.Stdout, os.Stderr)
		return
	}
	if showHelp(os.Args[1:]) {
		fmt.Print(usageText())
		return
	}
	// The bare binary used to BE the brain. It still starts it, for one
	// release, so a systemd unit that invokes exactly this argv keeps
	// working while it is moved to corral-wrangler — the daemon's own binary
	// — and this path then becomes a pointer. See internal/wranglerd.
	fmt.Fprintln(os.Stderr, "corral: the coordination server now lives in corral-wrangler; `corral` with no subcommand will stop starting it in the next release.")
	wranglerd.Run(version)
}

// wantsHelp reports whether argv is asking what a command does rather than
// asking it to do something.
//
// It exists because a subcommand that opens its data store before parsing
// answers `-h` with an IO error for anyone who has not run it yet — which is
// everyone, the first time. Help is the one request that must work on a bare
// machine.
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

// mcpUsage is what `corral mcp -h` prints. The subcommand takes no flags: it
// speaks MCP over stdin/stdout and is configured entirely by where the local
// findings store lives.
const mcpUsage = `corral mcp — serve corral's findings corpus over stdio MCP.

Usage:
  corral mcp

Speaks the Model Context Protocol on stdin/stdout so an editor or agent can
read what past audits found. READ-ONLY and local: no brain, no writes, no
network listener, and no adjudication surface (see mcp_findings.go for why).

Takes no flags. Reads the same local findings store ` + "`corral certify --local`" + ` writes.
`
