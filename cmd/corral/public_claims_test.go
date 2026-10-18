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

// Walk the instructional surfaces rather than enumerating the components
// that happened to carry these claims. Historical recordings and field notes
// are evidence of past runs, not current instructions, and are not rewritten.
func TestDocsPublicClaimsDoNotPromiseUnconditionalProof(t *testing.T) {
	root := publicClaimRoot(t)
	paths := []string{"README.md", "SECURITY.md"}
	for _, dir := range []string{"site/src/components", "site/src/content/docs/docs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && (strings.HasSuffix(path, ".astro") || strings.HasSuffix(path, ".mdx")) {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(paths) < 20 {
		t.Fatalf("instructional walk found only %d files", len(paths))
	}
	for _, path := range paths {
		text := publicClaimText(t, root, path)
		for _, claim := range []string{
			"every result is signed",
			"does not drift",
			"same model can't be surprised by its own bugs",
			"subject of the record doesn't control the ledger",
			"any fault that survives gets a missing test",
			"where corral is doing something the existing tools don't",
		} {
			if strings.Contains(text, claim) {
				t.Errorf("%s overstates the evidence: %q", path, claim)
			}
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
		"https://doi.org/10.1145/3832098",
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
