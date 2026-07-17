package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, "x/x.go"), "package x\nfunc F() bool { return true }\n")
	must(filepath.Join(dir, "x/x_test.go"), "package x\nimport \"testing\"\nfunc TestF(t *testing.T){ if !F(){t.Fatal(\"x\")} }\n")
	man := `{"corpus_version":"v1","targets":[
	  {"id":"x","code_path":"x/x.go","test_path":"x/x_test.go","goal":"F is true","test_cmd":"go test ./x/...","expected_adequacy":"thorough"}
	]}`
	mp := filepath.Join(dir, "manifest.json")
	must(mp, man)
	return mp
}

func TestLoadResolvesFilesAndDigest(t *testing.T) {
	mp := writeCorpus(t)
	m, err := Load(mp)
	if err != nil {
		t.Fatal(err)
	}
	if m.CorpusVersion != "v1" || len(m.Targets) != 1 {
		t.Fatalf("bad manifest: %+v", m)
	}
	tg := m.Targets[0]
	if tg.Code() == "" || tg.TestCode() == "" {
		t.Fatal("files not read")
	}
	d1 := tg.Digest()
	if len(d1) != 64 {
		t.Fatalf("digest not sha256 hex: %q", d1)
	}
	// Digest is stable and content-addressed: same inputs → same digest.
	m2, _ := Load(mp)
	if m2.Targets[0].Digest() != d1 {
		t.Fatal("digest not stable")
	}
}

func TestLoadRejectsMissingFileAndDupID(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, "m.json")
	os.WriteFile(mp, []byte(`{"corpus_version":"v1","targets":[{"id":"a","code_path":"nope.go","test_path":"nope_test.go","goal":"g","test_cmd":"go test"}]}`), 0o644)
	if _, err := Load(mp); err == nil {
		t.Fatal("expected error for missing file")
	}
}
