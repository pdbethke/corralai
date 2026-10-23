// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// The keyless fuzz fixture is committed, not generated per run, because a Go
// fuzz worker re-runs the Fuzz function in its own process: a fixture built
// from fresh random keys would give every worker a different CA, and a seed
// bundle would never verify in the process that mutates it. Verification
// uses the log's integrated time, never the clock, so a committed bundle does
// not expire. CORRAL_UPDATE_FIXTURES=1 regenerates it (as for two-entries).
const keylessFuzzDir = "testdata/keyless-fuzz"

func TestKeylessFuzzFixture(t *testing.T) {
	dir := filepath.FromSlash(keylessFuzzDir)
	if os.Getenv("CORRAL_UPDATE_FIXTURES") == "1" {
		s := newTestSigstore(t)
		art := make([]byte, 32)
		for i := range art {
			art[i] = byte(i)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, b := range map[string][]byte{"root.json": s.rootJSON, "bundle.json": s.issue(t, art, spec{}), "artifact.bin": art} {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	root, bundle, art := keylessFuzzFixture(t)
	if err := ledger.VerifyKeylessSignature(bundle, art, testRepo, root); err != nil {
		t.Fatalf("the committed seed bundle must verify (CORRAL_UPDATE_FIXTURES=1 regenerates it): %v", err)
	}
}

func keylessFuzzFixture(tb testing.TB) (*ledger.SigstoreRoot, []byte, []byte) {
	tb.Helper()
	dir := filepath.FromSlash(keylessFuzzDir)
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			tb.Fatal(err)
		}
		return b
	}
	root, err := ledger.ParseSigstoreRoot(read("root.json"))
	if err != nil {
		tb.Fatal(err)
	}
	return root, read("bundle.json"), read("artifact.bin")
}

// sigNameLine matches the signer NAME on a checkpoint signature line. The
// verifier deliberately ignores it (the key hint and the signature under the
// log's key bind the note, as in rekor; see verifyCheckpoint), so a bundle
// differing only there is the same statement. The first fuzz run found
// exactly that: a mutated name still verified.
var sigNameLine = regexp.MustCompile(`(?m)^— \S+ `)

func normalize(v any) any {
	switch x := v.(type) {
	case string:
		// encoding/base64 skips \r and \n, so a base64 field may carry them
		// and still decode to the same bytes (the second thing the fuzzer
		// found). A string is treated as base64 only if it decodes once they
		// are gone.
		if strings.ContainsAny(x, "\r\n") {
			if flat := strings.NewReplacer("\r", "", "\n", "").Replace(x); !strings.Contains(flat, " ") {
				if _, err := base64.StdEncoding.DecodeString(flat); err == nil {
					return flat
				}
			}
		}
		return sigNameLine.ReplaceAllString(x, "— NAME ")
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = normalize(x[k])
		}
	}
	return v
}

// jsonTree decodes b generically, keeping numbers as text so 1 and 1.0 differ.
func jsonTree(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return normalize(v), true
}

// FuzzKeylessBundle feeds arbitrary bytes as an entry's Sigstore bundle to
// the verifier, with a valid root, repository and artifact. It must not
// panic, and it may verify only a bundle that SAYS the same thing as the
// valid seed (whitespace and escapes may differ). A different document
// verifying means the verifier read something the signer did not sign.
func FuzzKeylessBundle(f *testing.F) {
	root, bundle, art := keylessFuzzFixture(f)
	f.Add(bundle)
	f.Add(append(append([]byte(nil), bundle...), '\n'))
	f.Add(bundle[:len(bundle)/2])
	f.Add(bytes.Replace(bundle, []byte(`"mediaType"`), []byte(`"MEDIATYPE"`), 1))
	f.Add(bytes.Replace(bundle, []byte(`{`), []byte(`{"mediaType":"x",`), 1))
	f.Add([]byte(`{}`))
	want, ok := jsonTree(bundle)
	if !ok {
		f.Fatal("seed bundle is not JSON")
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if err := ledger.VerifyKeylessSignature(b, art, testRepo, root); err != nil {
			return
		}
		got, ok := jsonTree(b)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("a bundle that differs from the signed seed verified:\n%s", b)
		}
	})
}
