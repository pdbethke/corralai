// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func publicClaimRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func publicClaimText(t *testing.T, root, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(string(b), "’", "'")), " ")
}

// publicClaimPaths walks the instructional surfaces rather than enumerating
// the components that happened to carry these claims: the top-level docs, the
// Action's user-visible input descriptions, the shipped docs/corral corpus,
// the video captions, and every site component, page and docs page. Field
// notes are deliberately NOT walked: they are dated evidence of past runs, and
// an overstated sentence there is kept beside a visible correction rather than
// rewritten, so banning the original phrase there would force a silent edit.
func publicClaimPaths(t *testing.T, root string) []string {
	t.Helper()
	paths := []string{"README.md", "SECURITY.md", "CORRAL.md", "action.yml",
		"docs/video/README.md", "docs/video/make-videos.py"}
	for _, dir := range []string{"site/src/components", "site/src/pages", "site/src/content/docs/docs", "docs/corral"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".astro", ".mdx", ".md":
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				if strings.HasPrefix(filepath.ToSlash(rel), "site/src/content/field-notes/") {
					t.Fatalf("the instructional walk reached a field note: %s", rel)
				}
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(paths) < 40 {
		t.Fatalf("instructional walk found only %d files", len(paths))
	}
	return paths
}

func TestDocsPublicClaimsDoNotPromiseUnconditionalProof(t *testing.T) {
	root := publicClaimRoot(t)
	for _, path := range publicClaimPaths(t, root) {
		text := publicClaimText(t, root, path)
		lower := strings.ToLower(text)
		for _, claim := range []string{
			"every result is signed",
			"does not drift",
			"same model can't be surprised by its own bugs",
			"subject of the record doesn't control the ledger",
			"any fault that survives gets a missing test",
			"where corral is doing something the existing tools don't",
			// The old hero line and its relatives: a test is ATTEMPTED for
			// each survivor, and a survivor the writer cannot prove stays
			// unproven.
			"for every bug that slips through, it writes a test",
			"writes a test for each survivor",
			"writes a test for every survivor",
			"test written for every survivor",
			"for every fault that survives writes a test",
			// Ledger entries are signed only when a certify key is present,
			// and tail truncation leaves a valid chain.
			"every entry is signed",
			"cannot have an entry removed quietly",
			"signed ledger**",
			"the signed ledger",
			"every signed verdict",
			"write signed, hash-linked entries",
			// The Action has no input for replaying recorded mutants.
			"replay recorded mutants when",
		} {
			if strings.Contains(lower, claim) {
				t.Errorf("%s overstates the evidence: %q", path, claim)
			}
		}
		if strings.Contains(text, "SIGNED LEDGER") {
			t.Errorf("%s calls a ledger that may be unsigned a SIGNED LEDGER", path)
		}
	}
}

// The ledger caveats a reader most needs are the ones the chain alone cannot
// show: the key holder can re-sign everything, and a stripped signature is a
// note, not a failure. Require the specific sentences, not loose words that
// any paragraph about ledgers would contain.
func TestDocsLedgerClaimsNameKeyHolderAndUnsignedNote(t *testing.T) {
	root := publicClaimRoot(t)
	for _, path := range []string{
		"SECURITY.md",
		"site/src/components/TheRecord.astro",
	} {
		text := publicClaimText(t, root, path)
		for _, required := range []string{
			"can rewrite and re-sign the whole chain",
			"a note, not a failure",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s must say %q", path, required)
			}
		}
	}
	text := publicClaimText(t, root, "docs/corral/github-action.md")
	for _, required := range []string{
		"breaks its hash until someone re-hashes and re-links the chain",
		"reports an unsigned entry among signed ones as a note, not a failure",
		"dropping the newest entries (tail truncation) leaves a valid chain",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("docs/corral/github-action.md must say %q", required)
		}
	}
}

// After the distinct-edits fix every printed interval names its n as distinct;
// an example that shows a bare (n=8) teaches a reader the old meaning.
func TestDocsIntervalExamplesCountDistinctEdits(t *testing.T) {
	root := publicClaimRoot(t)
	bare := regexp.MustCompile(`95% interval:? +[0-9.]+–[0-9.]+ \(n=[0-9]+\)`)
	for _, path := range publicClaimPaths(t, root) {
		if m := bare.FindString(publicClaimText(t, root, path)); m != "" {
			t.Errorf("%s shows an interval without its distinct count: %q", path, m)
		}
	}
	for _, path := range []string{"README.md", "site/src/content/docs/docs/first-audit.mdx"} {
		if !strings.Contains(publicClaimText(t, root, path), "(n=8 distinct)") {
			t.Errorf("%s must show the interval over distinct edits", path)
		}
	}
}

// A dated field note keeps its original sentence, so the correction beside it
// is the only thing standing between a reader and the overstated claim.
func TestFieldNotesCarryTheirCorrections(t *testing.T) {
	root := publicClaimRoot(t)
	for note, claim := range map[string]string{
		"two-videos-and-what-they-do-not-show": "rediscovers that gap is not guaranteed",
		"what-is-actually-new-here":            "Meta's ACH",
		"prove-your-tests-would-catch-it":      "the survivor stays unproven",
		"the-critic-was-never-the-point":       "the survivor stays unproven",
		"your-tests-suck":                      "the survivor stays unproven",
		"the-ledger-is-just-text":              "not every entry is signed",
	} {
		text := publicClaimText(t, root, "site/src/content/field-notes/"+note+".mdx")
		i := strings.Index(text, "> **Correction (2026-10-18):**")
		if i < 0 {
			t.Errorf("%s lost its dated correction", note)
			continue
		}
		if !strings.Contains(text[i:], claim) {
			t.Errorf("%s correction must say %q", note, claim)
		}
	}
}

func TestDocsLedgerClaimsNameSigningAndCompletenessLimits(t *testing.T) {
	root := publicClaimRoot(t)
	for _, path := range []string{
		"README.md", "SECURITY.md",
		"site/src/content/docs/docs/concepts/trust-and-security.mdx",
		"site/src/content/docs/docs/how-it-works.mdx",
		"site/src/components/TheRecord.astro",
	} {
		text := publicClaimText(t, root, path)
		for _, required := range []string{"unsigned", "tail", "external"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s must disclose %q in its ledger explanation", path, required)
			}
		}
	}
}

func TestDocsMutationMethodNamesPriorArtAndStatisticalLimits(t *testing.T) {
	text := publicClaimText(t, publicClaimRoot(t), "site/src/content/docs/docs/concepts/mutation-testing.mdx")
	for _, required := range []string{
		"https://arxiv.org/abs/2501.12862",
		"FSE 2025 Companion, Industry track",
		"https://doi.org/10.1145/3832098",
		"ISSTA 2026", "produced by Stryker", "as ACH also does",
		"independent", "representative", "probability that your code is correct",
		"fresh run", "authored_together", "warning",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("mutation-testing explanation must cover %q", required)
		}
	}
}

func TestDocsHomepageKeepsDiscoveryAndModelIndependenceQualified(t *testing.T) {
	root := publicClaimRoot(t)
	for _, path := range []string{"site/src/components/CiGate.astro", "site/src/components/TheVideos.astro"} {
		text := publicClaimText(t, root, path)
		if !strings.Contains(text, "fresh run") || !strings.Contains(text, "vary") {
			t.Errorf("%s must distinguish a recorded proof from variable fresh-run discovery", path)
		}
	}
	gate := publicClaimText(t, root, "site/src/components/CiGate.astro")
	for _, required := range []string{
		"fails only with a concrete fault and test in hand",
		"can miss a gap an earlier run found",
		"corral certify --mutants",
		"the Action has no input for that",
	} {
		if !strings.Contains(gate, required) {
			t.Errorf("CiGate must say %q", required)
		}
	}
	text := publicClaimText(t, root, "site/src/components/HowItWorks.astro")
	for _, required := range []string{"attempts", "not guaranteed", "independence"} {
		if !strings.Contains(text, required) {
			t.Errorf("homepage explanation must qualify %q", required)
		}
	}
}

// Parse the copyable YAML, not prose mentioning a model elsewhere on the page.
// This is a structural check, not a live provider or attestation trial.
func TestDocsHomepageActionExampleNamesSeatsAndAttestationPermissions(t *testing.T) {
	root := publicClaimRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "site/src/components/CiGate.astro"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?s)<pre[^>]*><code>(.*?)</code></pre>`).FindSubmatch(raw)
	if len(match) != 2 {
		t.Fatal("no copyable Action job example found")
	}
	var job struct {
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &job); err != nil {
		t.Fatal(err)
	}
	for _, step := range job.Steps {
		if !strings.HasPrefix(step.Uses, "pdbethke/corralai@") {
			continue
		}
		for _, seat := range []string{"writer-model", "mutant-model", "critic-model"} {
			if strings.TrimSpace(step.With[seat]) == "" {
				t.Errorf("the Action example does not select %s", seat)
			}
		}
		if step.With["goals"] == "" && step.With["derive-model"] == "" {
			t.Error("the Action example needs goals or derive-model")
		}
		if step.With["critic-model"] != "off" && step.With["critic-model"] == step.With["writer-model"] {
			t.Error("the Action example assigns the writer and critic the same model")
		}
		if step.With["attest"] == "true" {
			for permission, want := range map[string]string{
				"id-token": "write", "attestations": "write", "contents": "read",
			} {
				if got := job.Permissions[permission]; got != want {
					t.Errorf("attest example permission %s = %q, want %q", permission, got, want)
				}
			}
		}
		return
	}
	t.Fatal("no Corral Action step found in the example")
}

func TestDocsHomepageVerificationConsumesItsRequestedOutput(t *testing.T) {
	text := publicClaimText(t, publicClaimRoot(t), "site/src/components/Quickstart.astro")
	out := regexp.MustCompile(`--out\s+([A-Za-z0-9_.-]+)`).FindStringSubmatch(text)
	verify := regexp.MustCompile(`certify verify\s+([A-Za-z0-9_.-]+)\s+([^<]+)`).FindStringSubmatch(text)
	if len(out) != 2 || len(verify) != 3 {
		t.Fatal("the quickstart must request an output and show how to verify it")
	}
	if out[1] != verify[1] {
		t.Errorf("verify consumes %q but the preceding command writes %q", verify[1], out[1])
	}
	for _, flag := range []string{"--pubkey", "--allow-unanchored"} {
		if !strings.Contains(verify[2], flag) {
			t.Errorf("the local verification example needs %s", flag)
		}
	}
}
