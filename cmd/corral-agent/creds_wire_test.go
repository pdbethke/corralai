// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/creds"
	"github.com/zalando/go-keyring"
)

func TestAgentSecretResolvesFromStore(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("CORRAL_CREDS_DIR", dir)
	t.Setenv("CREDENTIALS_DIRECTORY", "") // no systemd cred in test
	st, _ := creds.Open()
	if err := st.Set("OPENAI_API_KEY", "sk-stored"); err != nil {
		t.Fatal(err)
	}
	// env unset → resolves from the store
	if got := agentSecret("OPENAI_API_KEY"); got != "sk-stored" {
		t.Fatalf("agentSecret = %q, want sk-stored", got)
	}
	// env wins
	t.Setenv("OPENAI_API_KEY", "sk-env")
	if got := agentSecret("OPENAI_API_KEY"); got != "sk-env" {
		t.Fatalf("env override: agentSecret = %q, want sk-env", got)
	}
	_ = filepath.Join(dir, "creds.age")
}
