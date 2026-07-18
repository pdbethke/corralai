// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/creds"
	"github.com/zalando/go-keyring"
)

// resetCredsMemoForTest clears the package-level creds memoization
// (credsStoreOnce/credsStore/scrubbedSecrets) so tests that exercise
// agentSecret/scrubSecretEnv don't leak state into each other via the
// process-lifetime sync.Once and sync.Map.
func resetCredsMemoForTest(t *testing.T) {
	t.Helper()
	credsStoreOnce = sync.Once{}
	credsStore = nil
	scrubbedSecrets = sync.Map{}
	t.Cleanup(func() {
		credsStoreOnce = sync.Once{}
		credsStore = nil
		scrubbedSecrets = sync.Map{}
	})
}

func TestAgentSecretResolvesFromStore(t *testing.T) {
	resetCredsMemoForTest(t)
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

// TestScrubSecretEnvUnsetsAndCaches verifies that scrubSecretEnv (1) removes
// the sensitive env vars from the process environment, and (2) leaves the
// pre-scrub value reachable through agentSecret afterward via the
// scrubbedSecrets cache — proving there's no read-after-unset data loss for
// callers (like the CORRALAI_BRAIN_KEY consumers) that read the secret only
// after scrub has already run.
func TestScrubSecretEnvUnsetsAndCaches(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	// Confine the creds store to a temp dir so resolving the untouched
	// canonical names (GEMINI_API_KEY etc.) during scrub can't read or write
	// the operator's real ~/.config/corral store.
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("OPENAI_API_KEY", "sk-before-scrub")
	t.Setenv("CORRALAI_BRAIN_KEY", "brain-before-scrub")

	scrubSecretEnv()

	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		t.Fatalf("OPENAI_API_KEY still set after scrub: %q", v)
	}
	if v := os.Getenv("CORRALAI_BRAIN_KEY"); v != "" {
		t.Fatalf("CORRALAI_BRAIN_KEY still set after scrub: %q", v)
	}

	if got := agentSecret("OPENAI_API_KEY"); got != "sk-before-scrub" {
		t.Fatalf("agentSecret(OPENAI_API_KEY) after scrub = %q, want sk-before-scrub (served from cache)", got)
	}
	if got := agentSecret("CORRALAI_BRAIN_KEY"); got != "brain-before-scrub" {
		t.Fatalf("agentSecret(CORRALAI_BRAIN_KEY) after scrub = %q, want brain-before-scrub (served from cache)", got)
	}
}
