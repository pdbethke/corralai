// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// machineLedgerWith writes a ledger directory of one scan entry per
// ModelsByRole/SeatTools pair, each with one audited file (195 survivors,
// 193 proven), signed with a throwaway key so the chain verifies.
func machineLedgerWith(t *testing.T, scans ...[2]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	auditpush.SetLedgerSigner(auditpush.Ed25519LedgerSigner{KeyID: auditpush.KeyFingerprint(priv.Public().(ed25519.PublicKey)), Key: priv})
	t.Cleanup(func() { auditpush.SetLedgerSigner(nil) })
	for i, sc := range scans {
		id := int64(i + 1)
		b := auditpush.Bundle{
			Scan: auditpush.ScanRow{Repo: "acme/r", Commit: fmt.Sprintf("c%07d", i), ScanID: id, Audited: 1},
			Files: []auditpush.Row{{
				Repo: "acme/r", ScanID: id, Path: "a.go", Disposition: "audited",
				Survivors: 195, ProvenMissed: 193, MutantsPlanted: 200, MutantsGraded: 200,
				ModelsByRole: sc[0], SeatTools: sc[1],
			}},
		}
		if _, err := auditpush.PushBundle(dir+"/", b); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func rankMachine(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	rc := runModels(append([]string{"rank"}, args...), t.TempDir(), defaultRankLoader, &out, &errb)
	return rc, out.String(), errb.String()
}

func TestRankReadsTheMachineLedgerByDefault(t *testing.T) {
	dir := machineLedgerWith(t, [2]string{`{"test-writer":"claude-sonnet-5-5"}`, ""})
	t.Setenv("CORRALAI_MACHINE_LEDGER", dir)
	rc, out, errb := rankMachine(t, "--seat", "test-writer", "--min-runs", "1")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb)
	}
	for _, want := range []string{"claude-sonnet-5-5", "193/195 survivors proven over 1 runs", "evidence: the machine ledger " + dir + " (1 scan(s); a run is one SCAN)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRankWithNoMachineLedgerSaysSoAndCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never", "made")
	t.Setenv("CORRALAI_MACHINE_LEDGER", dir)
	rc, out, errb := rankMachine(t)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb)
	}
	if !strings.Contains(out, "(not created yet: no run on this machine has written an entry)") {
		t.Errorf("output does not say the ledger is not created yet:\n%s", out)
	}
	if strings.Contains(out, "bug-catching") {
		t.Errorf("an absent machine ledger fell back to the scorecard:\n%s", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("reading the machine ledger created %s (stat err %v)", dir, err)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Errorf("reading the machine ledger created its parent (stat err %v)", err)
	}
}

func TestRankRefusesAMachineLedgerThatDoesNotVerify(t *testing.T) {
	dir := machineLedgerWith(t, [2]string{`{"test-writer":"claude-sonnet-5-5"}`, ""})
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("setup: %v, %d entries", err, len(entries))
	}
	path := filepath.Join(dir, auditpush.ScansSubdir, entries[0].File)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(raw, []byte(`"ProvenMissed": 193`), []byte(`"ProvenMissed": 194`), 1)
	if bytes.Equal(edited, raw) {
		t.Fatal("setup: nothing was edited")
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(edited)
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORRALAI_MACHINE_LEDGER", dir)
	rc, out, errb := rankMachine(t, "--min-runs", "1")
	if rc != 1 {
		t.Fatalf("rc=%d, want 1; stdout=%s stderr=%s", rc, out, errb)
	}
	if !strings.Contains(errb, dir) || !strings.Contains(errb, "does not verify") {
		t.Errorf("stderr must name the directory and say it does not verify:\n%s", errb)
	}
	if strings.Contains(out, "prefer:") {
		t.Errorf("a ranking was printed off a record that does not verify:\n%s", out)
	}
}

func TestRankSourceScorecardReadsTheOldStore(t *testing.T) {
	isolateStateHome(t)
	t.Setenv("CORRALAI_MACHINE_LEDGER", filepath.Join(t.TempDir(), "absent"))
	rc, out, errb := rankMachine(t, "--source", "scorecard")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb)
	}
	if !strings.Contains(out, "the local bug-catching ledger") {
		t.Errorf("--source scorecard did not read the bug-catching store:\n%s", out)
	}
}

func TestRankRefusesContradictorySources(t *testing.T) {
	rc, _, errb := rankMachine(t, "--db", t.TempDir(), "--source", "ledger")
	if rc != 2 || !strings.Contains(errb, "--db") || !strings.Contains(errb, "--source") {
		t.Errorf("--db with --source: rc=%d stderr=%s, want 2 naming both flags", rc, errb)
	}
	rc, _, errb = rankMachine(t, "--source", "nonsense")
	if rc != 2 || !strings.Contains(errb, "ledger, scorecard") {
		t.Errorf("--source nonsense: rc=%d stderr=%s, want 2 listing the sources", rc, errb)
	}
}

// An agent seat ("claude-code:claude-sonnet-5-5", a tool recorded for the
// role) and an API seat ("claude-sonnet-5-5") are different evidence and
// stay different rows.
func TestRankKeepsAnAgentSeatAndAnAPISeatApart(t *testing.T) {
	api, _ := json.Marshal(map[string]string{"test-writer": "claude-sonnet-5-5"})
	agent, _ := json.Marshal(map[string]string{"test-writer": "claude-code:claude-sonnet-5-5"})
	tools, _ := json.Marshal(map[string]string{"test-writer": "claude-code"})
	dir := machineLedgerWith(t, [2]string{string(api), ""}, [2]string{string(agent), string(tools)})
	t.Setenv("CORRALAI_MACHINE_LEDGER", dir)
	rc, out, errb := rankMachine(t, "--seat", "test-writer", "--min-runs", "1", "--json")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb)
	}
	if n := strings.Count(out, `"model": "claude-sonnet-5-5"`); n != 1 {
		t.Errorf("want one row for the API seat, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, `"model": "claude-code:claude-sonnet-5-5"`); n != 1 {
		t.Errorf("want one row for the agent seat, got %d:\n%s", n, out)
	}
}
