// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The provider each seat was served from is recorded on every signed entry
// that records the seat's model. The resolver has always decided it; until
// this record carried it, `openai/gpt-x` and `anthropic/gpt-x` signed the same
// bare roster and the entry could not say where the audited source went.

// providerSeats declares a writer in the explicit vendor/model form and a
// mutant generator that OLLAMA_URL's daemon lists, the two ways a seat's
// provider is decided that most often differ on one run.
func providerSeats(t *testing.T) {
	t.Helper()
	clearSeatEnv(t)
	t.Setenv("OLLAMA_URL", "http://127.0.0.1:1")
	t.Setenv("OPENAI_API_KEY", "test-placeholder-not-a-real-key")
	fakeOllama(t, "qwen3:8b")
}

// wantProviders fails unless got carries exactly the writer on openai and the
// generator on ollama.
func wantProviders(t *testing.T, where string, got map[string]string) {
	t.Helper()
	if got[advpool.RoleTestWriter] != "openai" || got[advpool.RoleMutantGenerator] != "ollama" {
		t.Errorf("%s: seat providers = %v, want test-writer=openai and mutant-generator=ollama", where, got)
	}
}

// rowProviders decodes a ledger row's SeatProviders JSON text.
func rowProviders(t *testing.T, raw string) map[string]string {
	t.Helper()
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("SeatProviders %q does not parse: %v", raw, err)
	}
	return m
}

// verdictProviders decodes the seat_providers of a row's verdict document.
func verdictProviders(t *testing.T, verdictJSON string) map[string]string {
	t.Helper()
	var doc struct {
		SeatProviders map[string]string `json:"seat_providers"`
	}
	if err := json.Unmarshal([]byte(verdictJSON), &doc); err != nil {
		t.Fatalf("verdict document does not parse: %v", err)
	}
	return doc.SeatProviders
}

// The resolver's decision reaches the RunSpec both verdict construction
// sites take it from.
func TestCertifySeatProvidersReachTheRunSpec(t *testing.T) {
	providerSeats(t)
	r, err := resolveRoles(t, localAuditInput{mutantModel: "qwen3:8b", writerModel: "openai/gpt-x", criticModel: "off", shadowModel: "off"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	wantProviders(t, "RunSpec", newAuditRunSpec(localAuditInput{}, r, runSubject{}).SeatProviders)
}

// certify --local (and so corral demo, which runs it): the signed entry read
// back from the machine ledger carries the providers on the file row and in
// the verdict document the row holds.
func TestLocalEntryRecordsTheSeatProviders(t *testing.T) {
	root, setPath, _ := localEntryFixture(t)
	providerSeats(t)
	ml := withMachineLedger(t)
	code, out, errs := runLocalEntryFixture(t, root, setPath, "--writer-model", "openai/gpt-x", "--mutant-model", "qwen3:8b")
	localRunFinished(t, code, out, errs)
	entries := scanEntriesIn(t, ml)
	if len(entries) != 1 || len(entries[0].Bundle.Files) != 1 {
		t.Fatalf("want one entry with one file row, got %d entries\n%s%s", len(entries), out, errs)
	}
	row := entries[0].Bundle.Files[0]
	wantProviders(t, "file row", rowProviders(t, row.SeatProviders))
	wantProviders(t, "verdict document", verdictProviders(t, row.VerdictJSON))
}

// certify --repo: the repo ledger's entry and its machine copy, read back,
// carry the providers on every file row.
func TestRepoEntryRecordsTheSeatProviders(t *testing.T) {
	root, setPath, goals := telemetryRepoFixture(t)
	providerSeats(t)
	machine := withMachineLedger(t)
	att := filepath.Join(t.TempDir(), "statement.json")
	code, out, errs := runTelemetryFixture(t, root, setPath, goals, "--writer-model", "openai/gpt-x", "--mutant-model", "qwen3:8b", "--attest", att)
	if code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out, errs)
	}
	// The --attest statement signs the same providers beside its roster.
	raw, err := os.ReadFile(att) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("reading the statement: %v", err)
	}
	var stmt struct {
		Predicate struct {
			SeatProviders map[string]string `json:"seatProviders"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(raw, &stmt); err != nil {
		t.Fatalf("statement does not parse: %v\n%s", err, raw)
	}
	wantProviders(t, "--attest statement", stmt.Predicate.SeatProviders)
	for name, dir := range map[string]string{"repo": filepath.Join(root, ".corral", "ledger"), "machine": machine} {
		entries, err := auditpush.ReadLedgerDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s ledger: %d entries, %v", name, len(entries), err)
		}
		files := entries[0].Bundle.Files
		if len(files) == 0 {
			t.Fatalf("%s ledger: no file rows", name)
		}
		audited := 0
		for _, f := range files {
			wantProviders(t, name+" "+f.Path+" row", rowProviders(t, f.SeatProviders))
			if f.Disposition != "audited" {
				continue
			}
			audited++
			// Every audited file carries its verdict; a row without one
			// would let this check pass on nothing.
			if f.VerdictJSON == "" {
				t.Fatalf("%s %s: the row carries no verdict document", name, f.Path)
			}
			wantProviders(t, name+" "+f.Path+" verdict", verdictProviders(t, f.VerdictJSON))
		}
		if audited != 2 {
			t.Fatalf("%s ledger: %d audited rows, want the fixture's two", name, audited)
		}
	}
}

// The verdict cache keys each seat with its provider. A second scan naming
// the same providers is served from cache and its rows still say where the
// cached verdict was measured; a scan naming the SAME bare model through a
// DIFFERENT provider is not served that verdict, so no entry carries a
// verdict from one provider under a row naming another.
func TestRepoCacheIsNotServedAcrossProviders(t *testing.T) {
	root, setPath, goals := telemetryRepoFixture(t)
	providerSeats(t)
	t.Setenv("CORRALAI_MACHINE_LEDGER", filepath.Join(t.TempDir(), "machine"))
	ledger := filepath.Join(t.TempDir(), "ledger")
	// One fixed, empty prior for every scan. Unset, the ledger itself is the
	// prior, its digest is in the cache key, and a second scan sits a
	// different exam and can never hit; that is the ledger's own rule and
	// not what this test is about.
	prior := t.TempDir()
	scan := func(writer string) auditpush.LedgerEntry {
		t.Helper()
		code, out, errs := runTelemetryFixture(t, root, setPath, goals, "--writer-model", writer, "--mutant-model", "qwen3:8b", "--ledger", ledger, "--prior", prior)
		if code != 0 {
			t.Fatalf("%s: exit=%d\n%s%s", writer, code, out, errs)
		}
		entries, err := auditpush.ReadLedgerDir(ledger)
		if err != nil || len(entries) == 0 {
			t.Fatalf("ledger: %d entries, %v", len(entries), err)
		}
		return entries[len(entries)-1]
	}
	check := func(e auditpush.LedgerEntry, wantHit bool, wantWriter string) {
		t.Helper()
		audited := 0
		for _, f := range e.Bundle.Files {
			if f.Disposition != "audited" {
				continue
			}
			audited++
			if f.CacheHit != wantHit {
				t.Errorf("%s (writer on %s): CacheHit = %v, want %v", f.Path, wantWriter, f.CacheHit, wantHit)
			}
			if f.VerdictJSON == "" {
				t.Fatalf("%s: the row carries no verdict document", f.Path)
			}
			row, verdict := rowProviders(t, f.SeatProviders), verdictProviders(t, f.VerdictJSON)
			if row[advpool.RoleTestWriter] != wantWriter || verdict[advpool.RoleTestWriter] != wantWriter {
				t.Errorf("%s: writer provider row %q, verdict %q; want both %q — one entry must not disagree with itself about where the source went",
					f.Path, row[advpool.RoleTestWriter], verdict[advpool.RoleTestWriter], wantWriter)
			}
		}
		if audited != 2 {
			t.Fatalf("writer on %s: %d audited rows, want the fixture's two", wantWriter, audited)
		}
	}
	check(scan("openai/gpt-x"), false, "openai")
	check(scan("openai/gpt-x"), true, "openai")
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	check(scan("anthropic/gpt-x"), false, "anthropic")
}

// The cache key differs for one bare model served by two providers, and
// stays byte-identical to the pre-provider key for a seat with none.
func TestModelSetKeyNamesEachSeatsProvider(t *testing.T) {
	ollama := modelSetKeyWith(nil, map[string]string{advpool.RoleTestWriter: "ollama"}, "gpt-x", "m", "off", "off", "")
	openai := modelSetKeyWith(nil, map[string]string{advpool.RoleTestWriter: "openai"}, "gpt-x", "m", "off", "off", "")
	if ollama == openai {
		t.Fatalf("one key for gpt-x on ollama and on openai: %q", ollama)
	}
	if bare := modelSetKeyWith(nil, nil, "gpt-x", "m", "off", "off", ""); bare != modelSetKey("gpt-x", "m", "off", "off", "") {
		t.Fatalf("a seat with no provider must key as its spec alone: %q", bare)
	}
	// Each key name reads its own role's provider.
	all := modelSetKeyWith(nil, map[string]string{
		advpool.RoleTestWriter: "pw", advpool.RoleMutantGenerator: "pm", advpool.RoleTestCritic: "pc",
		advpool.RoleMutantGeneratorShadow: "ps", advpool.RoleTestWriterShadow: "pws",
	}, "w", "m", "c", "s", "ws")
	want := reposcan.CanonicalKV(map[string]string{
		"test-writer": "pw/w", "mutant-generator": "pm/m", "critic": "pc/c", "shadow": "ps/s", "test-writer-shadow": "pws/ws",
	})
	if all != want {
		t.Fatalf("key = %q, want %q", all, want)
	}
}

// corral review: the reviewer's provider is recorded beside its model, and the
// verifier's beside its own when the verifier's answer is recorded. Read back
// from the ledger the run wrote.
func TestReviewEntryRecordsTheSeatProviders(t *testing.T) {
	requireReviewSandbox(t)
	providerSeats(t)
	t.Setenv("CORRALAI_MACHINE_LEDGER", filepath.Join(t.TempDir(), "machine"))
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(rs agentbackend.ResolvedSeat, _ agentbackend.SeatOptions) (agentbackend.Backend, error) {
		if rs.Spec == "gpt-x" {
			return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"c","tier":"CODE-READ","file":"pkg/a.go","line":1}],"sound":[]}`}, nil
		}
		return cannedReviewer{reply: `{"summary":"looks fine to me"}`}, nil
	}
	ledger := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "openai/gpt-x", "--verifier-model", "qwen3:8b", "--ledger", ledger}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ledger: %d entries, %v", len(entries), err)
	}
	r := entries[0].Review
	if r.VerifierModel == "" {
		t.Fatalf("precondition: the verifier's answer must be recorded in this test:\n%s", errb.String())
	}
	if r.SeatProviders[modelrank.SeatReviewer] != "openai" || r.SeatProviders[modelrank.SeatVerifier] != "ollama" {
		t.Errorf("review seat providers = %v, want reviewer=openai and verifier=ollama", r.SeatProviders)
	}
}

// A challenger DRAWN from a pool is resolved like a named one: the draw writes
// the member as typed (vendor/model included) into the shadow seat before the
// seats resolve, so its provider reaches the RunSpec under
// mutant-generator-shadow, beside the primary generator's own.
func TestADrawnShadowSeatRecordsItsProvider(t *testing.T) {
	providerSeats(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	f := flagsWith("", "openai/gpt-a,anthropic/claude-b", "", "", "0x2a")
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", f, nil, filepath.Join(t.TempDir(), "none.duckdb"), &bytes.Buffer{})
	if err != nil || len(sels) != 1 {
		t.Fatalf("draw: %v, %+v", err, sels)
	}
	want := map[string]string{"openai/gpt-a": "openai", "anthropic/claude-b": "anthropic"}[sels[0].Chosen]
	if want == "" {
		t.Fatalf("the draw chose %q, not a pool member as typed", sels[0].Chosen)
	}
	r, err := resolveRoles(t, localAuditInput{mutantModel: "qwen3:8b", writerModel: "openai/gpt-x", criticModel: "off", shadowModel: *f.model}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	got := newAuditRunSpec(localAuditInput{}, r, runSubject{}).SeatProviders
	if got[advpool.RoleMutantGeneratorShadow] != want || got[advpool.RoleMutantGenerator] != "ollama" {
		t.Fatalf("providers = %v, want mutant-generator-shadow=%s (drawn %s) and mutant-generator=ollama", got, want, sels[0].Chosen)
	}
}
