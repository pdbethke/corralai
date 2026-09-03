// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE `TestDocs` PREFIX ON THESE THREE IS LOAD-BEARING, NOT A NAMING WHIM.
//
// deploy.yml runs `go test ./cmd/corral/ -run "^TestDocs"` with NO `if:` guard,
// and gates everything else behind a filter that classifies an all-Markdown
// diff as docs-only. These three police what MARKDOWN claims, so outside that
// prefix they were unreachable by the one change class they exist to catch: a
// docs-only PR adding an unexecuted flag to the README passed CI.
//
// That is the identical defect TestDocsGatesRunOnDocsOnlyChanges was written to
// fix for the pin gate, recurring in the gate built to replace it — which is
// why the property is now asserted rather than remembered, below.
//
// TestDocsEverySurfaceIsClassified is the gate on the executed-surface manifest.
//
// THE DEFECT IT EXISTS TO PREVENT: `--jail container` was advertised in the
// README as the macOS/Windows path and had never been executed once. It was
// broken in the most basic way available. A claim written from intent rather
// than from a run is indistinguishable, on the page, from one that was earned.
//
// So every flag corral exposes must be CLASSIFIED — executed, attested, or
// unexecuted. The manifest does not demand everything be proven; it demands
// that nobody can add a surface and leave the question unasked. A new flag
// with no row fails here, and answering costs one line.
//
// The enumeration comes from docs/cli/*.md rather than a hand-kept list,
// because scripts/gen-cli-docs.sh --check already guarantees those files are
// what the binaries really print — and a hand-kept scope is the exact defect
// that left six subcommands with 24 undocumented flags.
func TestDocsEverySurfaceIsClassified(t *testing.T) {
	exposed := exposedSurfaces(t)
	if len(exposed) == 0 {
		t.Fatal("read zero surfaces from docs/cli — this gate is not looking where it thinks it is")
	}
	classified, order := manifestRows(t)

	for _, s := range order {
		if _, ok := exposed[s]; !ok {
			t.Errorf("manifest classifies %q, which no binary exposes any more — a stale row is a claim about something that is gone", s)
		}
	}
	var missing []string
	for s := range exposed {
		if _, ok := classified[s]; !ok {
			missing = append(missing, s)
		}
	}
	sort.Strings(missing)
	for _, s := range missing {
		t.Errorf("%s is exposed and unclassified — add a row to testdata/executed-surfaces.tsv saying whether anything has ever run it. \"unexecuted\" is a legal answer; not answering is not", s)
	}
}

// TestDocsClassifiedSurfacesCarryAReceipt: a surface claimed as run must name what
// ran it. "executed" with an empty receipt is the fabricated-confidence failure
// this whole manifest exists to refuse, committed by the manifest itself.
func TestDocsClassifiedSurfacesCarryAReceipt(t *testing.T) {
	classified, order := manifestRows(t)
	for _, s := range order {
		r := classified[s]
		switch r.status {
		case "executed", "attested":
			if r.receipt == "" {
				t.Errorf("%s is marked %q with no receipt — name the test or the run, or mark it unexecuted", s, r.status)
				continue
			}
			// A RECEIPT THAT NAMES NOTHING REAL IS NOT A RECEIPT. Before this
			// check the gate tested only that the string was non-empty, so
			// "x" satisfied it — which made the one-way property claimed in
			// TestDocsUnexecutedSurfacesAreNotAdvertised's comment ("the cheap
			// way out costs a receipt someone can read") false. A reviewer
			// found exactly that.
			//
			// Deliberately NOT asserting that the named test contains the flag
			// literal: the documented meaning of a `test:` receipt is a
			// pointer to the code that exercises the surface, and a flag whose
			// VALUE is threaded into a function is usually tested through that
			// function with the flag string appearing nowhere. Demanding the
			// literal would push authors toward the weaker receipt that
			// happens to mention it. Existence is the floor; the reading is
			// still a human's job.
			switch {
			case strings.HasPrefix(r.receipt, "test:"):
				rel := strings.TrimPrefix(strings.Fields(r.receipt)[0], "test:")
				if _, err := os.Stat(filepath.Join("..", "..", rel)); err != nil {
					t.Errorf("%s cites %q, which does not exist — a receipt must point at something a reader can open", s, rel)
				}
			case strings.HasPrefix(r.receipt, "run:"):
				// A dated run, e.g. "run:2026-09-02 record 89". The date is
				// what makes it auditable later: a receipt with no date cannot
				// be tied to a ledger row or a warehouse scan.
				if !regexp.MustCompile(`^run:\d{4}-\d{2}-\d{2}\b`).MatchString(r.receipt) {
					t.Errorf("%s has receipt %q — a run receipt must start `run:YYYY-MM-DD` so it can be traced to a ledger row", s, r.receipt)
				}
			default:
				t.Errorf("%s has receipt %q — a receipt must begin `test:<path>` or `run:<date> <id>`, so it names something checkable rather than asserting one exists", s, r.receipt)
			}
		case "unexecuted":
			if r.receipt != "" {
				t.Errorf("%s is marked unexecuted but carries receipt %q — one of the two is wrong", s, r.receipt)
			}
		default:
			t.Errorf("%s has unknown status %q — want executed, attested or unexecuted", s, r.status)
		}
	}
}

type surfaceRow struct{ status, receipt string }

func manifestRows(t *testing.T) (map[string]surfaceRow, []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "executed-surfaces.tsv"))
	if err != nil {
		t.Fatalf("opening the manifest: %v", err)
	}
	defer f.Close() //nolint:errcheck

	out := map[string]surfaceRow{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for ln := 1; sc.Scan(); ln++ {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			t.Fatalf("manifest line %d is not surface<TAB>status<TAB>receipt: %q", ln, line)
		}
		receipt := ""
		if len(parts) > 2 {
			receipt = parts[2]
		}
		if _, dup := out[parts[0]]; dup {
			t.Errorf("manifest classifies %q twice — two answers to one question", parts[0])
		}
		out[parts[0]] = surfaceRow{status: parts[1], receipt: receipt}
		order = append(order, parts[0])
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	return out, order
}

// exposedSurfaces reads every flag out of the GENERATED CLI reference, keyed as
// "<command> --<flag>". Flags before the first `## \`x\` flags` heading belong
// to the binary itself.
func exposedSurfaces(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join("..", "..", "docs", "cli")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	head := regexp.MustCompile("^## `([a-z-]+(?: [a-z-]+| --[a-z-]+)*)` flags")
	flag := regexp.MustCompile(`^\s+-([a-z][a-z0-9-]*)`)

	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("reading %s: %v", e.Name(), rerr)
		}
		section := strings.TrimSuffix(e.Name(), ".md")
		for _, line := range strings.Split(string(b), "\n") {
			if m := head.FindStringSubmatch(line); m != nil {
				section = m[1]
				continue
			}
			if m := flag.FindStringSubmatch(line); m != nil {
				out[section+" --"+m[1]] = true
			}
		}
	}
	return out
}

// userFacingDocs are the files a stranger reads BEFORE running anything: the
// front page, the shipped knowledge corpus, the site, and the Action's own
// contract.
//
// The GENERATED CLI reference is exempt wherever it lives — docs/cli/ and the
// site's mirror of it. It documents every flag by construction, including the
// ones nothing has run, and that is precisely its job: it is the enumeration
// this manifest is built FROM. Exempting it is not a loophole, because it
// makes no persuasive claim; it is a list of what exists.
var userFacingDocs = []string{
	"README.md",
	"docs/corral",
	"site/src/content/docs",
	"action.yml",
}

// TestDocsUnexecutedSurfacesAreNotAdvertised is the launch gate the manifest was
// built for.
//
// `--jail container` was named in the README's platform table as the
// macOS/Windows path while nothing had ever run it, and it was broken in the
// most basic way available. The manifest answers "has anything run this?"; this
// test is what makes the answer BINDING — a surface nobody has exercised must
// not appear in the material that persuades someone to try corral.
//
// It is deliberately a ONE-WAY gate. Marking a surface `executed` requires a
// receipt (TestDocsClassifiedSurfacesCarryAReceipt), and only then may it be
// advertised. So the cheap way out — flipping a row to `executed` to silence
// this — costs a receipt someone can read, which is the whole point.
//
// SCOPE, stated because a gate trusted past its scope is worse than none: this
// matches a flag's exact spelling in prose. It cannot see a capability claimed
// in words that never name the flag ("works on macOS"), which is what the
// platform table did. That case stays a human judgement; this closes the
// mechanical half.
func TestDocsUnexecutedSurfacesAreNotAdvertised(t *testing.T) {
	classified, _ := manifestRows(t)

	// A flag is forbidden in prose only when EVERY command exposing that
	// spelling is unexecuted.
	//
	// This gate matches a flag's spelling, not its owning command — a limit its
	// SCOPE paragraph already states — so a name shared across binaries must be
	// judged across all of them. Without this, `corral-recordings-import --db`
	// being unexecuted made the README's documented `corral seal --db`
	// unmentionable, and the gate's advice ("stop naming it where a stranger
	// will read it first") would have been actively wrong. A gate that fires on
	// a legitimate claim teaches people to route around it.
	anyExecuted := map[string]bool{}
	unexecutedByFlag := map[string]bool{}
	for surface, row := range classified {
		i := strings.LastIndex(surface, " --")
		if i < 0 {
			continue
		}
		flag := surface[i+1:]
		if row.status == "unexecuted" {
			unexecutedByFlag[flag] = true
		} else {
			anyExecuted[flag] = true
		}
	}
	var unexecuted []string
	for flag := range unexecutedByFlag {
		if !anyExecuted[flag] {
			unexecuted = append(unexecuted, flag)
		}
	}
	sort.Strings(unexecuted)
	if len(unexecuted) == 0 {
		return // nothing unexecuted: the gate has nothing to say
	}

	root := filepath.Join("..", "..")
	for _, target := range userFacingDocs {
		err := filepath.WalkDir(filepath.Join(root, target), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // a missing optional doc tree is not this gate's business
			}
			if d.IsDir() {
				return nil
			}
			if strings.Contains(filepath.ToSlash(path), "/docs/cli/") {
				return nil // the generated reference; see userFacingDocs
			}
			switch strings.ToLower(filepath.Ext(d.Name())) {
			case ".md", ".mdx", ".yml", ".yaml", ".astro":
			default:
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			body := string(b)
			rel, _ := filepath.Rel(root, path)
			for _, flag := range unexecuted {
				// Word-bounded: `--swarm` must not match `--swarming`.
				re := regexp.MustCompile(regexp.QuoteMeta(flag) + `($|[^a-zA-Z0-9-])`)
				if re.MatchString(body) {
					t.Errorf("%s advertises %s, which the manifest marks unexecuted.\n"+
						"Either run it and record a receipt in testdata/executed-surfaces.tsv, or stop naming it where a stranger will read it first.\n"+
						"This is the gate that `--jail container` needed: named in the README as the macOS/Windows path, never once executed, and broken.",
						rel, flag)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", target, err)
		}
	}
}

// TestDocsEveryDocPolicingTestRunsOnDocsOnlyChanges generalises the fix for a
// defect that has now shipped twice.
//
// deploy.yml runs `-run "^TestDocs"` unguarded and puts everything else behind
// a docs-only filter. So a test that polices what MARKDOWN says is only ever
// reached if it is NAMED for that selector. The pin gate lost this fight once;
// the surfaces gates lost it again the day after they were written.
//
// A name is a bad place to keep a load-bearing property, so this asserts it
// instead: any test in this package that reads the repository's user-facing
// documentation must carry the prefix CI selects on. It finds them by AST —
// a test that walks docs mentions one of the doc roots by name — so a NEW doc
// gate is caught the moment it is written rather than the day someone notices
// CI never ran it.
func TestDocsEveryDocPolicingTestRunsOnDocsOnlyChanges(t *testing.T) {
	const prefix = "TestDocs"
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing this package's tests: %v", err)
	}

	// IDENTIFIERS, not filenames. A filename marker like "README.md" also
	// matches the many tests that WRITE a README into a temp fixture, which
	// read nothing and would drown the real signal. These are the helpers in
	// this package that read the REPOSITORY's own documentation, so naming one
	// is what makes a test a doc gate.
	markers := []string{"docsAdvertisingAnActionRef", "userFacingDocs", "manifestRows", "exposedSurfaces", "docGateSelector"}
	found := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				reads := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.Ident:
						for _, m := range markers {
							if v.Name == m {
								reads = true
							}
						}
					}
					return !reads
				})
				if !reads {
					continue
				}
				found++
				if !strings.HasPrefix(fn.Name.Name, prefix) {
					t.Errorf("%s reads the repository's documentation but is not named %q*, so CI's docs-only step (`-run \"^%s\"`) never runs it — a Markdown-only PR would sail past the gate it exists to be.",
						fn.Name.Name, prefix, prefix)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("found no test reading the documentation — this gate is not looking where it thinks it is")
	}
}
