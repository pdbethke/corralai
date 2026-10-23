// SPDX-License-Identifier: Elastic-2.0

package ledgertest_test

import (
	"crypto/sha256"
	"os/exec"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/ledger/ledgertest"
)

// The fixture is judged by both verifiers: corral's own, and sigstore-go's
// under the same policy, so a bundle that passes only because the fixture
// and the verifier share a mistake is caught.
func TestIssuedBundleVerifies(t *testing.T) {
	s := ledgertest.New(t)
	art := sha256.Sum256([]byte("an entry hash"))
	b := s.Issue(t, art[:], "acme/widgets")
	if err := ledger.VerifyKeylessSignature(b, art[:], "acme/widgets", s.Root(t)); err != nil {
		t.Fatalf("corral's verifier: %v", err)
	}
	if err := ledger.VerifyKeylessSignature(b, art[:], "acme/other", s.Root(t)); err == nil {
		t.Fatal("a bundle for acme/widgets must not verify for acme/other")
	}

	if err := s.Oracle(t, b, art[:], "acme/widgets"); err != nil {
		t.Fatalf("sigstore-go (the oracle): %v", err)
	}
	if err := s.Oracle(t, b, art[:], "acme/other"); err == nil {
		t.Fatal("the oracle accepted a bundle for another repository")
	}
}

// ledgertest mints a certificate for any repository it is asked to, so no
// package that ships may import it. The importers are derived from the
// module (go list's Imports holds non-test imports only), never listed.
func TestOnlyTestsImportLedgertest(t *testing.T) {
	const self = "github.com/pdbethke/corralai/internal/ledger/ledgertest"
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, "github.com/pdbethke/corralai/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	pkgs := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		pkgs++
		for _, imp := range f[1:] {
			if imp == self {
				t.Errorf("%s imports %s in non-test code; it is test support only", f[0], self)
			}
		}
	}
	// Floor the walk: a go list that matched nothing would pass vacuously.
	if pkgs < 20 {
		t.Fatalf("go list named only %d packages — the walk did not cover the module", pkgs)
	}
}
