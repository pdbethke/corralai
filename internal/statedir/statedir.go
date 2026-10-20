// SPDX-License-Identifier: Elastic-2.0

// Package statedir is the one place a corral file's location is decided.
//
// Every database, key and directory corral keeps is a row in the table
// below. A caller asks Path(row); it never joins a home directory itself.
// Before this package, 34 call sites each built their own default under
// another tool's dot-directory under $HOME — see
// docs/superpowers/specs/2026-10-12-state-dir-design.md for why that was
// wrong and why the roots are what they are.
//
// Three roots, because their contents have different lifetimes:
//   - State: history that cannot be regenerated (databases, signing keys).
//   - Cache: anything rebuildable (the console bundle, vendor staging,
//     the verdict cache derivable from the ledger).
//   - Config: the credential store, which already lived here.
//
// Resolution is read on every call, never cached, so t.Setenv works.
package statedir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Root is one of corral's base directories.
type Root int

const (
	State Root = iota
	Cache
	Config
)

func (r Root) String() string {
	switch r {
	case State:
		return "state"
	case Cache:
		return "cache"
	default:
		return "config"
	}
}

// OverrideVar is the environment variable that relocates the whole root.
// Config keeps CORRAL_CREDS_DIR — the one corral variable without the
// CORRALAI_ prefix — because renaming it would break every operator who set
// it and buy nothing.
func (r Root) OverrideVar() string {
	switch r {
	case State:
		return "CORRALAI_STATE_DIR"
	case Cache:
		return "CORRALAI_CACHE_DIR"
	default:
		return "CORRAL_CREDS_DIR"
	}
}

// File is one row: which root, the file's name in it, and the variable
// that relocates just this file (kept from before this package existed).
type File struct {
	Root     Root
	Name     string
	Override string
}

// The table. Adding a file corral keeps means adding a row here, never a
// filepath.Join at a call site; the gate in gate_test.go enforces it.
var (
	CoordDB          = File{State, "corralai_coord.sqlite3", "CORRALAI_DB"}
	CertifyKey       = File{State, "corralai_certify_key", "CORRALAI_CERTIFY_KEY_FILE"}
	BrainKey         = File{State, "corralai_brain_key", "CORRALAI_BRAIN_KEY_FILE"}
	MemoryDB         = File{State, "corralai_memory.duckdb", "CORRALAI_MEMORY_DB"}
	PrincipalsDB     = File{State, "corralai_principals.sqlite3", "CORRALAI_PRINCIPALS_DB"}
	GatewayDB        = File{State, "corralai_gateway.sqlite3", "CORRALAI_GATEWAY_DB"}
	ArtifactsDB      = File{State, "corralai_artifacts.sqlite3", "CORRALAI_ARTIFACTS_DB"}
	MissionDB        = File{State, "corralai_missions.sqlite3", "CORRALAI_MISSION_DB"}
	QueueDB          = File{State, "corralai_queue.sqlite3", "CORRALAI_QUEUE_DB"}
	ReferenceDB      = File{State, "corralai_reference.duckdb", "CORRALAI_REFERENCE_DB"}
	TelemetryDB      = File{State, "corralai_telemetry.duckdb", "CORRALAI_TELEMETRY_DB"}
	RecordingsDB     = File{State, "corralai_recordings.duckdb", "CORRALAI_RECORDINGS_DB"}
	LearnDB          = File{State, "corralai_learn.sqlite3", "CORRALAI_LEARN_DB"}
	TaskArtifactsDB  = File{State, "corralai_task_artifacts.sqlite3", "CORRALAI_TASK_ARTIFACTS_DB"}
	BuildDB          = File{State, "corralai_build.duckdb", "CORRALAI_BUILD_DB"}
	BugCatchDB       = File{State, "corralai_bugcatch.duckdb", "CORRALAI_BUGCATCH_DB"}
	CriticScoreDB    = File{State, "corralai_criticscore.duckdb", "CORRALAI_CRITICSCORE_DB"}
	MutantAttemptsDB = File{State, "corralai_mutant_attempts.duckdb", "CORRALAI_MUTANT_ATTEMPTS_DB"}
	MatrixDB         = File{State, "corralai_matrix.duckdb", "CORRALAI_MATRIX_DB"}
	RepoCodeDB       = File{State, "corralai_repocode.duckdb", "CORRALAI_REPOCODE_DB"}
	GateDB           = File{State, "corralai_gate.duckdb", "CORRALAI_GATE_DB"}
	ControlSpecDB    = File{State, "corralai_control_spec.duckdb", "CORRALAI_CONTROL_GATE_SPEC_DB"}
	ControlGateDB    = File{State, "corralai_control_gate.duckdb", "CORRALAI_CONTROL_GATE_DB"}
	CacheDB          = File{Cache, "corralai_cache.duckdb", "CORRALAI_CACHE_DB"}

	// MachineLedger is a DIRECTORY, not a file: this machine's own ledger,
	// where every run writes a complete entry whether or not the audited repo
	// keeps one. It lives in State (not the repo, not Cache) because it is
	// history, and "delete the cache" must never delete it. See IsDir for what
	// being a directory excludes it from.
	MachineLedger = File{State, "ledger", "CORRALAI_MACHINE_LEDGER"}
)

// IsDir reports whether the row names a directory. The table was files only;
// legacy migration moves files (and their database side files) and the legacy
// refusal guards a key that would otherwise be re-minted, and neither idea
// applies to a ledger directory that has no legacy home — so both skip these
// rows explicitly rather than treat a directory as a very large file.
func (f File) IsDir() bool { return f == MachineLedger }

var table = []File{
	CoordDB, CertifyKey, BrainKey, MemoryDB, PrincipalsDB, GatewayDB, ArtifactsDB,
	MissionDB, QueueDB, ReferenceDB, TelemetryDB, RecordingsDB, LearnDB,
	TaskArtifactsDB, BuildDB, BugCatchDB, CriticScoreDB, MutantAttemptsDB,
	MatrixDB, RepoCodeDB, GateDB, ControlSpecDB, ControlGateDB, CacheDB,
	MachineLedger,
}

// Files is the whole table, for tests, migrate and `corral state paths`.
func Files() []File { return append([]File(nil), table...) }

type platform struct {
	goos   string
	getenv func(string) string
	home   func() (string, error)
}

var host = platform{goos: runtime.GOOS, getenv: os.Getenv, home: os.UserHomeDir}

func (p platform) env(k string) string { return strings.TrimSpace(p.getenv(k)) }

// xdg returns $k/corral when $k is absolute; the XDG spec says a relative
// value is invalid and must be ignored.
func (p platform) xdg(k string) string {
	if v := p.env(k); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "corral")
	}
	return ""
}

// Dir resolves a root. It never falls back to a temp directory: a key or
// database written there is lost at reboot and readable by other users.
func Dir(r Root) (string, error) {
	p := host
	if v := p.env(r.OverrideVar()); v != "" {
		return v, nil
	}
	if p.goos == "windows" {
		// os.UserCacheDir is %LocalAppData% on Windows; giving State and
		// Cache their own subdirectories keeps "delete the cache" from
		// deleting a signing key (plan ruling R1).
		local, roaming := p.env("LocalAppData"), p.env("AppData")
		switch {
		case r == Config && roaming != "":
			return filepath.Join(roaming, "corral"), nil
		case r != Config && local != "":
			return filepath.Join(local, "corral", r.String()), nil
		}
		return "", noRoot(r, nil)
	}
	if d := p.xdg(map[Root]string{State: "XDG_STATE_HOME", Cache: "XDG_CACHE_HOME", Config: "XDG_CONFIG_HOME"}[r]); d != "" && p.goos != "darwin" {
		return d, nil
	}
	h, err := p.home()
	if err != nil || h == "" {
		return "", noRoot(r, err)
	}
	if p.goos == "darwin" {
		if r == Cache {
			return filepath.Join(h, "Library", "Caches", "corral"), nil
		}
		return filepath.Join(h, "Library", "Application Support", "corral"), nil
	}
	return filepath.Join(h, map[Root]string{State: ".local/state", Cache: ".cache", Config: ".config"}[r], "corral"), nil
}

func noRoot(r Root, cause error) error {
	return fmt.Errorf("corral has no %s directory (no HOME and no XDG/platform directory): set %s: %v", r, r.OverrideVar(), cause)
}

// Path resolves one row: its own override, else the root plus its name.
// It refuses a State row whose only copy is still at the legacy location
// (legacy.go) rather than let a caller create a fresh one beside it.
func Path(f File) (string, error) {
	if v := host.env(f.Override); v != "" {
		return v, nil
	}
	dir, err := Dir(f.Root)
	if err != nil {
		return "", fmt.Errorf("%s: %w", f.Name, err)
	}
	p := filepath.Join(dir, f.Name)
	if err := refuseLegacyOnly(f, p); err != nil {
		return "", err
	}
	return p, nil
}

// PathForWrite is Path for a caller about to open or create the file: it
// resolves (and refuses) exactly as Path does, then makes the parent
// directory 0700. DuckDB and SQLite create a missing FILE but not a missing
// directory, and on a fresh machine the state root does not exist yet — so
// without this every store opener failed, or dropped its rows silently.
//
// The parent is created only after the refusal has passed: a legacy-only
// file must leave nothing new behind. A per-file override's parent is made
// too; the operator named the file, and its directory is part of that. Path
// stays free of side effects (`corral state paths` and the tests read it).
func PathForWrite(f File) (string, error) {
	p, err := Path(f)
	if err != nil {
		return "", err
	}
	if err := EnsureParent(p); err != nil {
		return "", err
	}
	return p, nil
}

// EnsureParent makes p's directory 0700 if it is missing. It is the one
// spelling of that step, for an opener whose path came from a flag rather
// than the table.
func EnsureParent(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("creating the directory for %s: %w", p, err)
	}
	return nil
}
