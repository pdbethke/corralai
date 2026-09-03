// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// THE FLEET TABLE IS A CLAIM ABOUT EVERY BINARY, AND IT WAS WRONG ABOUT THREE.
//
// README.md's fleet table has a CGO column, and the prose beneath it says the
// brain's two CGO deps "make it the one binary that cares about its platform".
// That is the architecture: the brain owns the databases, and everything else
// is a client that reaches it over MCP/HTTP.
//
// On 2026-09-03 the table said "no" for corral-observe, corral-admin and
// corral-desktop, and all three needed cgo. internal/console — the reverse
// proxy those three share — imported internal/ui to reach two symbols, and
// internal/ui imports 21 internal packages, so every client linked DuckDB,
// SQLite and twelve tree-sitter grammars. corral-observe was 115 MB and bound
// to libstdc++.
//
// Nothing caught it because nothing executed the claim. deploy/observe/
// Dockerfile builds with CGO_ENABLED=0 onto distroless/static — a base image
// with no libc at all — so it could not have built, and the binary could not
// have run if it had. No CI job built it. The comment at the top of that
// Dockerfile ("Pure Go (no CGO) — it is just a credentialed reverse proxy")
// was not a lie; it was TRUE WHEN WRITTEN, and one import made it false years
// of commits later, silently, because a claim in prose costs nothing to break.
//
// So this executes the column. Both directions, which is the part that matters:
// a "no" row must build without cgo, and a "yes" row must NOT. Checking only
// the "no" rows would leave the gate defeatable by editing the table to say
// "yes" everywhere — the enumeration failure mode this repository has now been
// bitten by four times, arriving as a doc edit rather than a code edit.
//
// The TestDocs prefix is load-bearing: deploy.yml runs `-run '^TestDocs'`
// unguarded and puts the rest of the suite behind a docs-only filter, so a
// README-only edit to this very column reaches this test and nothing else.
func TestDocsFleetTableCGOColumnIsTrue(t *testing.T) {
	rows := fleetTableRows(t)
	for _, r := range rows {
		t.Run(r.binary, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join("..", "..", "cmd", r.binary)
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("README's fleet table names %q, but %s does not exist: %v", r.binary, dir, err)
			}
			cmd := exec.Command("go", "build", "-o", os.DevNull, "./cmd/"+r.binary)
			cmd.Dir = filepath.Join("..", "..")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()

			switch {
			case r.cgo && err == nil:
				t.Errorf("README's fleet table says %s needs CGO (yes), but it builds fine with CGO_ENABLED=0.\n"+
					"The table is now understating what ships as a pure-Go client. Change its CGO cell to `no` —\n"+
					"and check whether the Platforms table and the deploy/ Dockerfile for it can be simplified too.", r.binary)
			case !r.cgo && err != nil:
				t.Errorf("README's fleet table says %s is CGO-free (no), but CGO_ENABLED=0 cannot build it:\n%s\n"+
					"Something now reaches the engine from a client. Find the edge with:\n"+
					"    go list -deps ./cmd/%s | grep -E 'duckdb|tree-sitter|sqlite'\n"+
					"and sever it — a client verifies data, it does not need the engine that produced it.\n"+
					"Do NOT fix this by editing the README to say `yes`: that ships a 115 MB reverse proxy\n"+
					"and breaks its distroless image, which is exactly the state this gate was written for.",
					r.binary, strings.TrimSpace(string(out)), r.binary)
			}
		})
	}
}

// fleetRow is one parsed row of README.md's fleet table.
type fleetRow struct {
	binary string
	cgo    bool
}

// fleetTableRows parses README.md's fleet table into (binary, needs-cgo) pairs.
//
// It is a HELPER, not an inline parse, because
// TestDocsEveryDocPolicingTestRunsOnDocsOnlyChanges finds doc gates by looking
// for identifiers like this one in a test's AST. Naming it here is what makes
// the gate above visible to the meta-gate that keeps doc gates reachable in CI.
//
// The floor matters as much as the parse: a table this walk cannot find yields
// zero rows, and a range over zero rows passes. Every walk in this package
// carries one for that reason.
func fleetTableRows(t *testing.T) []fleetRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	// A row looks like: | **`corral-top`** | the ... | no | binary / `go install` |
	row := regexp.MustCompile("(?m)^\\|\\s*\\*\\*`(corral[a-z-]*)`\\*\\*\\s*\\|([^|]*)\\|\\s*(yes|no)\\s*\\|")
	var rows []fleetRow
	for _, m := range row.FindAllStringSubmatch(string(b), -1) {
		rows = append(rows, fleetRow{binary: m[1], cgo: m[3] == "yes"})
	}
	if len(rows) == 0 {
		t.Fatal("parsed ZERO rows out of README.md's fleet table — this gate is not looking where it thinks it is.\n" +
			"Either the table moved/changed shape, or its CGO column is gone. Fix the parse; do not delete the gate.")
	}
	// The table describes the whole fleet, so it should account for every
	// binary in cmd/. Derived, not hardcoded: a NEW binary that the table
	// never mentions is precisely the one whose CGO claim nobody checked.
	ents, err := os.ReadDir(filepath.Join("..", "..", "cmd"))
	if err != nil {
		t.Fatalf("read cmd/: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.binary] = true
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "corral") {
			continue
		}
		// Internal build tools are not part of the shipped fleet.
		if _, err := os.Stat(filepath.Join("..", "..", "cmd", e.Name(), "main.go")); err != nil {
			continue
		}
		if !seen[e.Name()] {
			t.Errorf("cmd/%s exists but README's fleet table never lists it, so nothing states or checks "+
				"whether it needs CGO. Add a row (Binary | Role | CGO | Ships as).", e.Name())
		}
	}
	return rows
}

// EVERY IMAGE IN THIS REPOSITORY FAILED TO BUILD, FOR ONE REASON.
//
// go.mod requires go >= 1.26.6. All five Dockerfiles said `FROM golang:1.26`,
// which ships 1.26.5 and sets GOTOOLCHAIN=local, so `go mod download` refused
// to fetch a newer toolchain and every build died on its first real step:
//
//	go: go.mod requires go >= 1.26.6 (running go 1.26.5; GOTOOLCHAIN=local)
//
// A floating minor tag looks like the careful choice — it picks up patches —
// but it drifts BELOW a floor that only ever moves up, and it does so without
// any commit to this repository. Nothing noticed because no workflow builds an
// image; the Dockerfiles were shipped as documentation of an intent.
//
// The comparison is derived from go.mod on both sides, so bumping the module's
// floor fails this test until the images follow. That is the point: the floor
// moving is exactly when the images silently stop building.
func TestDockerfilesUseAGoAtLeastTheModuleFloor(t *testing.T) {
	root := filepath.Join("..", "..")
	floor := goModFloor(t, root)

	base := regexp.MustCompile(`(?m)^FROM\s+golang:([0-9]+(?:\.[0-9]+)*)`)
	checked := 0

	// THE TRACKED SET, not a filesystem walk. A plain walk descends into
	// .worktrees/ — sibling checkouts of OTHER branches — and reports their
	// Dockerfiles as this commit's problem. `git ls-files` is the derivation
	// that matches what this commit actually ships, and it stays correct when
	// someone adds an image somewhere no hardcoded directory list mentions.
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable (not a git checkout?): %v", err)
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || !strings.HasPrefix(filepath.Base(rel), "Dockerfile") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel)) // #nosec G304 -- rel comes from git ls-files on this repository
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, m := range base.FindAllStringSubmatch(string(b), -1) {
			checked++
			if cmpVersion(parseVersion(m[1]), floor) < 0 {
				t.Errorf("%s builds on golang:%s, but go.mod requires go >= %s.\n"+
					"The golang images set GOTOOLCHAIN=local, so this image cannot run `go mod download` at all.\n"+
					"Pin the base to the module floor (golang:%s...) rather than a floating minor tag.",
					rel, m[1], strings.Join(itoaAll(floor), "."), strings.Join(itoaAll(floor), "."))
			}
		}
	}
	if checked == 0 {
		t.Fatal("found ZERO `FROM golang:` lines in any Dockerfile — this gate is not looking where it thinks " +
			"it is. Either the images moved, or they no longer build Go from a golang base. Fix the walk.")
	}
}

// goModFloor returns the `go` directive from go.mod as version components.
func goModFloor(t *testing.T, root string) []int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	m := regexp.MustCompile(`(?m)^go\s+([0-9]+(?:\.[0-9]+)*)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("go.mod has no `go` directive — cannot derive the toolchain floor")
	}
	return parseVersion(m[1])
}

func parseVersion(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return out
		}
		out = append(out, n)
	}
	return out
}

// cmpVersion compares two version component lists. A SHORTER base version is
// treated as lower when its components tie: `golang:1.26` is not `1.26.0`, it
// is "whatever 1.26.x the registry serves today", which is precisely the
// floating tag that drifted below the floor. Refusing to call it equal is what
// makes this gate reject a floating minor tag rather than accept it.
func cmpVersion(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func itoaAll(v []int) []string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return out
}
