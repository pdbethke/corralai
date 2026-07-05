// SPDX-License-Identifier: Elastic-2.0

package egress

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan_PlantedAWSKeyBlocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.go", "package config\n\nconst key = \"AKIAABCDEFGHIJKLMNOP\"\n")

	findings := Scan(context.Background(), dir, []string{"config.go"})
	if len(findings) == 0 {
		t.Fatal("expected a finding for the planted AWS key, got none")
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityBlock && f.Path == "config.go" {
			found = true
			if f.Sample == "" {
				t.Error("finding must carry a (redacted) sample")
			}
			// The raw secret must never appear in the finding output.
			if wantAbsent := "AKIAABCDEFGHIJKLMNOP"; contains(f.Sample, wantAbsent) {
				t.Errorf("finding leaked the raw secret: %q", f.Sample)
			}
		}
	}
	if !found {
		t.Fatalf("expected a blocking finding on config.go, got: %+v", findings)
	}
}

func TestScan_PlantedPrivateKeyBlocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "id_rsa", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----\n")

	findings := Scan(context.Background(), dir, []string{"id_rsa"})
	blocking := 0
	for _, f := range findings {
		if f.Severity == SeverityBlock {
			blocking++
		}
	}
	if blocking == 0 {
		t.Fatalf("expected a blocking finding for the planted private key, got: %+v", findings)
	}
}

func TestScan_CleanChangeSetPasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")
	writeFile(t, dir, "README.md", "# hello world\n\nThis is a clean change.\n")

	findings := Scan(context.Background(), dir, []string{"main.go", "README.md"})
	for _, f := range findings {
		if f.Severity == SeverityBlock {
			t.Fatalf("expected no blocking findings on a clean change set, got: %+v", f)
		}
	}
}

func TestScan_OnlyScansChangedFiles(t *testing.T) {
	dir := t.TempDir()
	// A secret sits in a file that is NOT part of the changed set — it must be
	// ignored. Only files explicitly passed in are scanned.
	writeFile(t, dir, "unrelated.go", "package x\n\nconst k = \"AKIAABCDEFGHIJKLMNOP\"\n")
	writeFile(t, dir, "clean.go", "package x\n\nfunc F() {}\n")

	findings := Scan(context.Background(), dir, []string{"clean.go"})
	for _, f := range findings {
		if f.Path == "unrelated.go" {
			t.Fatalf("scan touched a file outside the changed set: %+v", f)
		}
	}
}

func TestScan_LicenseAdvisory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "vendor/thing/LICENSE", "GNU GENERAL PUBLIC LICENSE\nVersion 3\n")

	findings := Scan(context.Background(), dir, []string{"vendor/thing/LICENSE"})
	found := false
	for _, f := range findings {
		if f.Severity == SeverityAdvisory && f.Path == "vendor/thing/LICENSE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an advisory finding for the GPL license file, got: %+v", findings)
	}
}

func TestScan_MissingFileSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	// deleted-in-diff file: does not exist on disk. Must not panic or error.
	findings := Scan(context.Background(), dir, []string{"gone.go"})
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a missing file, got: %+v", findings)
	}
}

func contains(s, substr string) bool {
	return len(substr) > 0 && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
