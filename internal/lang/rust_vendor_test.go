// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRustVendor pins when the Rust plugin asks for vendoring and where the
// source-replacement config goes. It never runs the command.
func TestRustVendor(t *testing.T) {
	var v Vendorer = rustPlugin{}
	argv := []string{"cargo", "vendor", "--locked", "vendor"}

	if step, ok := v.Vendor(t.TempDir()); ok || step.Argv != nil {
		t.Fatalf("no Cargo.toml: got %+v, %v", step, ok)
	}

	crate := t.TempDir()
	writeFile(t, filepath.Join(crate, "Cargo.toml"), "[package]\nname = \"x\"\n")
	step, ok := v.Vendor(crate)
	if !ok || !slices.Equal(step.Argv, argv) || step.StdoutAppend != ".cargo/config.toml" || step.Done != "" {
		t.Fatalf("crate: got %+v, %v", step, ok)
	}

	// A config that does something else entirely is not "already vendored".
	writeFile(t, filepath.Join(crate, ".cargo", "config.toml"), "[alias]\nx = \"build\"\n")
	if step, ok := v.Vendor(crate); !ok || step.StdoutAppend != ".cargo/config.toml" {
		t.Fatalf("crate with an unrelated config: got %+v, %v", step, ok)
	}

	// cargo reads the legacy extensionless file INSTEAD of config.toml when
	// both exist, so the replacement must go where cargo will read it.
	legacy := t.TempDir()
	writeFile(t, filepath.Join(legacy, "Cargo.toml"), "[package]\nname = \"x\"\n")
	writeFile(t, filepath.Join(legacy, ".cargo", "config"), "[alias]\nx = \"build\"\n")
	if step, ok := v.Vendor(legacy); !ok || step.StdoutAppend != ".cargo/config" {
		t.Fatalf("crate with a legacy .cargo/config: got %+v, %v", step, ok)
	}

	// Already vendored: the config already replaces crates-io with cargo
	// vendor's own source name, in either file.
	for _, name := range []string{"config.toml", "config"} {
		done := t.TempDir()
		writeFile(t, filepath.Join(done, "Cargo.toml"), "[package]\nname = \"x\"\n")
		writeFile(t, filepath.Join(done, ".cargo", name), "[source.crates-io]\nreplace-with = \"vendored-sources\"\n\n[source.vendored-sources]\ndirectory = \"vendor\"\n")
		if step, ok := v.Vendor(done); ok || step.Argv != nil {
			t.Fatalf("already vendored via .cargo/%s: got %+v, %v", name, step, ok)
		}
	}
}
