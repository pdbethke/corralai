// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pdbethke/corralai/internal/tomllite"
)

// rustWalkDepth caps the upward search for a Cargo.toml. The walk also stops
// at the repository directory, which is the tighter bound in practice; this
// only guards a pathologically deep path.
const rustWalkDepth = 32

// cargoCrateName is what cargo accepts as a package name. The name is read
// from a file the repository controls and lands in argv, so anything else —
// above all a leading '-', which cargo would parse as a flag — is refused.
var cargoCrateName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// cargoSelectsPackages reports whether the operator's command already chooses
// what cargo grades. Only words BEFORE a `--` count: after it they belong to
// the test binary.
func cargoSelectsPackages(cmd []string) bool {
	for _, a := range cmd {
		if a == "--" {
			return false
		}
		switch {
		case a == "-p", a == "--package", a == "--workspace", a == "--all", a == "--manifest-path":
			return true
		case strings.HasPrefix(a, "--package="), strings.HasPrefix(a, "--manifest-path="):
			return true
		case strings.HasPrefix(a, "-p") && !strings.HasPrefix(a, "--"): // -pCRATE
			return true
		}
	}
	return false
}

// ScopeCmd makes a Rust audit grade ONE crate, and say which. `cargo test`
// from a workspace root grades every member, so the suite a mutant is measured
// against would be the whole workspace's — a different, far costlier and
// unrelated measurement from "the tests of the crate this file is in". The
// owning crate is the nearest Cargo.toml above the file that has a
// `[package] name` (a manifest with both [workspace] and [package] is a
// package), searched no higher than the repository directory.
//
// Both runners get the same treatment from this one function: `-p <crate>` is
// cargo's own flag, which `cargo test` and `cargo nextest run` both take. It is
// inserted before a `--` (what follows is the test binary's) and otherwise
// appended. An operator command that already selects packages is returned
// unchanged and needs no manifest to be found: that choice is theirs.
func (rustPlugin) ScopeCmd(repoDir, codePath string, testCmd []string) ([]string, error) {
	if cargoSelectsPackages(testCmd) {
		return testCmd, nil
	}
	// -p is cargo's flag. A wrapper (make, just, a script, `sh -c`) would take
	// it as its own argument, so only a command whose program IS cargo (or
	// cargo-nextest, the other binary rustRunner accepts) is rewritten; the
	// program is found the way rustRunner finds it, past any VAR=value prefix.
	prog := preflightBin(testCmd, "")
	if prog == "" {
		return nil, fmt.Errorf("%s: the test command names no program corral could find, so corral cannot add the crate to grade; name it yourself with `-p <crate>` inside the command", codePath)
	}
	if bin := filepath.Base(prog); bin != "cargo" && bin != "cargo-nextest" {
		return nil, fmt.Errorf("%s: the test command runs %q, not cargo directly, so corral cannot add the crate to grade; name it yourself with `-p <crate>` inside the command", codePath, bin)
	}
	crate, err := owningCrate(repoDir, codePath)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(testCmd)+2)
	i := len(testCmd)
	for j, a := range testCmd {
		if a == "--" {
			i = j
			break
		}
	}
	out = append(out, testCmd[:i]...)
	out = append(out, "-p", crate)
	return append(out, testCmd[i:]...), nil
}

// owningCrate resolves the crate that owns codePath; see ScopeCmd.
func owningCrate(repoDir, codePath string) (string, error) {
	if strings.TrimSpace(repoDir) == "" {
		return "", fmt.Errorf("%s: there is no checkout to read a Cargo.toml from, so corral cannot say which crate owns it; name it with `-p <crate>` in the test command", codePath)
	}
	root, err := filepath.Abs(repoDir)
	if err != nil {
		return "", err
	}
	file := filepath.FromSlash(codePath)
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	dir := filepath.Dir(filepath.Clean(file))
	firstSeen := ""
	for {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			break // above (or outside) the repository: never searched
		}
		steps := 1 // the repo root itself
		if rel != "." {
			steps += len(strings.Split(rel, string(filepath.Separator)))
		}
		d, ok := nearestAncestorWith(dir, []string{"Cargo.toml"}, min(steps, rustWalkDepth))
		if !ok {
			break
		}
		manifest := filepath.Join(d, "Cargo.toml")
		if firstSeen == "" {
			firstSeen = manifest
		}
		text, err := os.ReadFile(manifest) // #nosec G304 -- a Cargo.toml found by walking up from the file under audit, inside the repository
		if err != nil {
			return "", fmt.Errorf("%s: reading %s: %v", codePath, manifest, err)
		}
		if name, ok := tomllite.StringValue(string(text), "package", "name"); ok {
			if !cargoCrateName.MatchString(name) {
				return "", fmt.Errorf("%s: %s names its package %q, which is not a crate name cargo accepts, so corral will not put it on a command line", codePath, manifest, name)
			}
			return name, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		dir = parent
	}
	if firstSeen == "" {
		return "", fmt.Errorf("%s: no Cargo.toml at or above it within the repository, so corral cannot say which crate to grade; name it with `-p <crate>` in the test command", codePath)
	}
	return "", fmt.Errorf("%s: the nearest manifest, %s, is a virtual manifest (a [workspace] with no [package] name) and no package manifest sits above the file, so corral cannot say which crate owns it; name it with `-p <crate>` in the test command", codePath, firstSeen)
}
