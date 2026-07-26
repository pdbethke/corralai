package reposcan

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestEnumerateClassifies(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go":      "package pkg\n",
		"pkg/a_test.go": "package pkg\n",
		"pkg/b.go":      "package pkg\n", // no paired test
		"README.md":     "# hi\n",        // no language
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(cands), cands)
	}
	if cands[0].Path != "pkg/a.go" || cands[0].TestPath != "pkg/a_test.go" || cands[0].Lang != "go" {
		t.Errorf("bad candidate: %+v", cands[0])
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["pkg/a_test.go"] != "is-test" {
		t.Errorf("a_test.go reason = %q, want is-test", reasons["pkg/a_test.go"])
	}
	if reasons["pkg/b.go"] != "no-paired-test" {
		t.Errorf("b.go reason = %q, want no-paired-test", reasons["pkg/b.go"])
	}
	if reasons["README.md"] != "no-language" {
		t.Errorf("README.md reason = %q, want no-language", reasons["README.md"])
	}
}

func TestEnumerateIsDeterministic(t *testing.T) {
	root := writeTree(t, map[string]string{
		"z/z.go": "package z\n", "z/z_test.go": "package z\n",
		"a/a.go": "package a\n", "a/a_test.go": "package a\n",
	})
	first, _, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Path != "a/a.go" || first[1].Path != "z/z.go" {
		t.Fatalf("candidates not sorted by path: %+v", first)
	}
}
