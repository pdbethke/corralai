// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEverySurfaceIsClassified is the gate on the executed-surface manifest.
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
func TestEverySurfaceIsClassified(t *testing.T) {
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

// TestClassifiedSurfacesCarryAReceipt: a surface claimed as run must name what
// ran it. "executed" with an empty receipt is the fabricated-confidence failure
// this whole manifest exists to refuse, committed by the manifest itself.
func TestClassifiedSurfacesCarryAReceipt(t *testing.T) {
	classified, order := manifestRows(t)
	for _, s := range order {
		r := classified[s]
		switch r.status {
		case "executed", "attested":
			if r.receipt == "" {
				t.Errorf("%s is marked %q with no receipt — name the test or the run, or mark it unexecuted", s, r.status)
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

// TestUnexecutedSurfacesAreNotAdvertised is the launch gate the manifest was
// built for.
//
// `--jail container` was named in the README's platform table as the
// macOS/Windows path while nothing had ever run it, and it was broken in the
// most basic way available. The manifest answers "has anything run this?"; this
// test is what makes the answer BINDING — a surface nobody has exercised must
// not appear in the material that persuades someone to try corral.
//
// It is deliberately a ONE-WAY gate. Marking a surface `executed` requires a
// receipt (TestClassifiedSurfacesCarryAReceipt), and only then may it be
// advertised. So the cheap way out — flipping a row to `executed` to silence
// this — costs a receipt someone can read, which is the whole point.
//
// SCOPE, stated because a gate trusted past its scope is worse than none: this
// matches a flag's exact spelling in prose. It cannot see a capability claimed
// in words that never name the flag ("works on macOS"), which is what the
// platform table did. That case stays a human judgement; this closes the
// mechanical half.
func TestUnexecutedSurfacesAreNotAdvertised(t *testing.T) {
	classified, _ := manifestRows(t)
	var unexecuted []string
	for surface, row := range classified {
		if row.status == "unexecuted" {
			// the flag itself, e.g. "corral certify --repo --swarm" -> "--swarm"
			if i := strings.LastIndex(surface, " --"); i >= 0 {
				unexecuted = append(unexecuted, surface[i+1:])
			}
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
