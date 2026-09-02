// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
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
