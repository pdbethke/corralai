// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// swiftBranchPins reads Package.swift for dependencies pinned to a branch,
// in the two forms SwiftPM's manifest API spells it, and nothing written in a
// comment or a string literal.
func TestSwiftBranchPins(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"labelled branch", `let package = Package(name: "P", dependencies: [
    .package(url: "https://example.com/a.git", branch: "main"),
    .package(url: "https://example.com/b.git", from: "1.0.0"),
])`, []string{"https://example.com/a.git"}},
		{"older .branch requirement", `dependencies: [.package(url: "https://example.com/c.git", .branch("develop"))]`, []string{"https://example.com/c.git"}},
		{"versions and revisions are no branch", `dependencies: [
    .package(url: "https://example.com/a.git", from: "1.0.0"),
    .package(url: "https://example.com/b.git", revision: "abc123"),
    .package(url: "https://example.com/c.git", exact: "2.0.0"),
]`, nil},
		{"a branch in a comment", `dependencies: [
    // .package(url: "https://example.com/a.git", branch: "main"),
    /* .package(url: "https://example.com/b.git", branch: "main") */
    .package(url: "https://example.com/c.git", from: "1.0.0"),
]`, nil},
		{"a branch in a string", `dependencies: [.package(url: "https://example.com/branch: main", from: "1.0.0")]
let note = ".package(url: \"x\", branch: \"main\")"`, nil},
		{"a branch whose url is not a plain literal", `let u = "https://example.com/a.git"
dependencies: [.package(url: u, branch: "main")]`, []string{"a dependency"}},
		{"name label before url", `dependencies: [.package(name: "A", url: "https://example.com/a.git", branch: "main")]`, []string{"https://example.com/a.git"}},
		{"qualified call", `dependencies: [Package.Dependency.package(url: "https://example.com/q.git", branch: "main")]`, []string{"https://example.com/q.git"}},
		{"another type's package member", `let x = Other.package(url: "https://example.com/o.git", branch: "main")`, nil},
		{"source that ends inside a comment", `dependencies: [.package(url: "https://example.com/a.git", branch: "main")] /*`, nil},
	}
	for _, c := range cases {
		if got := swiftBranchPins(c.src); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The swift row's scan line names branch-pinned dependencies when there is
// no Package.resolved beside the manifest to fix their revisions, and is
// silent when there is one, when nothing is pinned to a branch, and when the
// command builds a package elsewhere (--package-path), whose manifest is not
// the one read here. What a workspace tree costs is not the runner's to say:
// the pool says it, for every language with a build directory, and only when
// there is more than one tree.
func TestSwiftRunDisclosures(t *testing.T) {
	p, _ := ByName("swift")
	branchy := `// swift-tools-version:5.9
import PackageDescription
let package = Package(name: "P", dependencies: [.package(url: "https://example.com/a.git", branch: "main")])
`
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	branchLine := func(lines []string) string {
		for _, l := range lines {
			if strings.Contains(l, "branch") && strings.Contains(l, "Package.resolved") {
				return l
			}
		}
		return ""
	}

	unresolved := t.TempDir()
	write(unresolved, "Package.swift", branchy)
	got := RunDisclosures(p, unresolved, Cmd([]string{"swift", "test"}))
	if len(got) != 1 {
		t.Errorf("want only the branch line, got %q", got)
	}
	if l := branchLine(got); !strings.Contains(l, "https://example.com/a.git") {
		t.Errorf("branch pin, no Package.resolved: want a line naming the url, got %q", got)
	}

	resolved := t.TempDir()
	write(resolved, "Package.swift", branchy)
	write(resolved, "Package.resolved", "{}\n")
	if got := RunDisclosures(p, resolved, Cmd([]string{"swift", "test"})); len(got) != 0 {
		t.Errorf("with Package.resolved: want nothing, got %q", got)
	}

	unpinned := t.TempDir()
	write(unpinned, "Package.swift", `let package = Package(name: "P", dependencies: [.package(url: "https://example.com/a.git", from: "1.0.0")])`)
	if got := RunDisclosures(p, unpinned, Cmd([]string{"swift", "test"})); branchLine(got) != "" {
		t.Errorf("no branch pin: got %q", got)
	}

	if got := RunDisclosures(p, unresolved, Cmd([]string{"swift", "test", "--package-path", "Other"})); branchLine(got) != "" {
		t.Errorf("--package-path names another package: got %q", got)
	}
	if got := RunDisclosures(p, unresolved, Cmd([]string{"swift", "test", "--package-path=Other"})); branchLine(got) != "" {
		t.Errorf("--package-path= names another package: got %q", got)
	}

	// The manifest is found at or above the repository directory, as
	// Preflight finds it.
	sub := filepath.Join(unresolved, "Sources")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := RunDisclosures(p, sub, Cmd([]string{"swift", "test"})); branchLine(got) == "" {
		t.Errorf("manifest above the repository directory: got %q", got)
	}
}
