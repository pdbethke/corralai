// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/tomllite"
)

// cargoLocateTimeout bounds the one `cargo locate-project` call LockfileDir
// makes. It answered in under 0.1 s in every case measured (cargo 1.90); the
// bound is for a cargo that hangs, which then counts as no answer.
const cargoLocateTimeout = 30 * time.Second

// Lockfile is cargo's Cargo.lock beside a Cargo.toml, found by walking up
// as ScopeCmd walks for the owning crate. Each workspace tree builds its own
// target/ (BuildDirs), and a tree without the checkout's Cargo.lock resolves
// the dependencies afresh: measured on cargo 1.90, a crate whose Cargo.lock
// pinned itoa 1.0.1 built itoa 1.0.19 in a copy without it.
func (rustPlugin) Lockfile() Lockfile {
	return Lockfile{Name: "Cargo.lock", Manifest: "Cargo.toml", SearchDepth: rustWalkDepth}
}

// LockfileDir is the workspace root cargo names for the package in
// manifestDir (`cargo locate-project --workspace`), where cargo keeps its
// Cargo.lock. cargo's answer, not a second reading of members, exclude,
// globs and package.workspace: measured on cargo 1.90, it named the root for
// a member listed by a glob and for one whose package.workspace names it,
// and the package itself for one in workspace.exclude, each where building
// it wrote Cargo.lock. The cargo on PATH answers, not necessarily the one
// the test command names; the layout rules it reads have not changed across
// the versions this was run on (one, 1.90).
//
// No answer (no cargo, a timeout, a manifest cargo refuses, as a package
// inside a workspace that neither lists nor excludes it, which does not
// build either) is manifestDir: see Lockfiler.LockfileDir.
func (rustPlugin) LockfileDir(manifestDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), cargoLocateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cargo", "locate-project", "--workspace", "--message-format", "plain", "--offline")
	cmd.Dir = manifestDir
	// rustup's proxy would install a toolchain a rust-toolchain.toml names
	// and the machine lacks before cargo answers; this question is not worth
	// a download (the test command, run later, still installs it if rustup
	// is set to). WaitDelay keeps a child that holds stdout from outliving
	// the timeout.
	cmd.Env = append(os.Environ(), "RUSTUP_AUTO_INSTALL=0")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return manifestDir
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) || filepath.Base(p) != "Cargo.toml" {
		return manifestDir
	}
	return filepath.Dir(p)
}

// ManifestPins reports whether the Cargo.toml in manifestDir gives Cargo.lock
// something to pin: the word "dependencies" anywhere in it ([dependencies],
// [dev-dependencies], [build-dependencies], a target's, dependencies.x = …,
// an inline table under target = {...}; a mention in a comment or a string
// counts too, erring toward pinning), or a [workspace] (a table or a key),
// whose members' manifests are not read here. A package with none resolves
// nothing, so its trees agree without a lockfile. A manifest that cannot be
// read counts as pinning.
func (rustPlugin) ManifestPins(manifestDir string) bool {
	text, err := os.ReadFile(filepath.Join(manifestDir, "Cargo.toml")) // #nosec G304 -- the manifest LockfileFor found inside the checkout under audit; only read
	if err != nil {
		return true
	}
	if strings.Contains(string(text), "dependencies") {
		return true
	}
	for _, path := range tomllite.Paths(string(text)) {
		if path == "workspace" || strings.HasPrefix(path, "workspace.") {
			return true
		}
	}
	return false
}
