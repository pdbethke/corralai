// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/models"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

func flagsWith(model, pool, wModel, wPool, seed string) *shadowSeatFlags {
	return &shadowSeatFlags{model: &model, pool: &pool, writerModel: &wModel, writerPool: &wPool, seed: &seed}
}

// Names with no cloud-vendor prefix resolve as local (ForModelOrLocal), so
// these tests need no key.
func TestDrawShadowSeatsWritesTheDrawIntoTheSeat(t *testing.T) {
	f := flagsWith("", "localmodel-a:1,localmodel-b:1", "", "", "0x2a")
	var errb bytes.Buffer
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", f, nil, filepath.Join(t.TempDir(), "none.duckdb"), &errb)
	if err != nil {
		t.Fatal(err)
	}
	if len(sels) != 1 || sels[0].Role != advpool.RoleMutantGeneratorShadow || *f.model != sels[0].Chosen {
		t.Fatalf("selection %+v, seat now %q", sels, *f.model)
	}
	if !strings.Contains(errb.String(), "challenger drawn from a pool of 2") || !strings.Contains(errb.String(), "0x000000000000002a") {
		t.Fatalf("stderr does not disclose the draw: %q", errb.String())
	}
}

func TestDrawShadowSeatsReadsHistoryForTheLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bc.duckdb")
	s, err := bugcatch.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1, 0).UTC()
	_ = s.Record(context.Background(), []bugcatch.Observation{
		{TS: ts, RecordID: 1, Model: "localmodel-a:1", Role: "test-writer", Lang: "go", Catches: 3, Opportunities: 4},
	})
	s.Close()
	f := flagsWith("", "", "", "localmodel-a:1,localmodel-b:1", "0x1")
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", f, nil, path, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	m := sels[0].Members[0]
	if sels[0].History != shadowpool.HistoryRead || sels[0].HistoryRows != 1 || m.Alpha != 4 || m.Beta != 2 {
		t.Fatalf("selection %+v", sels[0])
	}
}

func TestDrawShadowSeatsUnreadableStoreIsNotRead(t *testing.T) {
	dir := t.TempDir() // a directory is not a DuckDB file
	f := flagsWith("", "localmodel-a:1,localmodel-b:1", "", "", "0x1")
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", f, nil, dir, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("an unreadable scorecard must not stop the run: %v", err)
	}
	if sels[0].History != shadowpool.HistoryNotRead || sels[0].HistoryRows != 0 {
		t.Fatalf("an unread history must be labelled, not zero: %+v", sels[0])
	}
}

func TestDrawShadowSeatsRefusals(t *testing.T) {
	cases := []struct{ name, model, pool, wModel, wPool, seed, want string }{
		{"both named and pooled", "a", "localmodel-a:1,localmodel-b:1", "", "", "", "--shadow-model"},
		{"writer both", "", "", "a", "localmodel-a:1,localmodel-b:1", "", "--shadow-writer-model"},
		{"pool of one", "", "localmodel-a:1", "", "", "", "at least two"},
		{"bad seed", "", "localmodel-a:1,localmodel-b:1", "", "", "zz", "--shadow-seed"},
		{"uncredentialed member", "", "localmodel-a:1,claude-opus-5-5", "", "", "", "claude-opus-5-5"},
	}
	orig := shadowMemberRunnable
	t.Cleanup(func() { shadowMemberRunnable = orig })
	shadowMemberRunnable = func(m string) error {
		if strings.HasPrefix(m, "claude-") {
			return fmt.Errorf("ANTHROPIC_API_KEY is not set")
		}
		return nil
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith(c.model, c.pool, c.wModel, c.wPool, c.seed), nil, filepath.Join(t.TempDir(), "x.duckdb"), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestDrawShadowSeatsSeedWithoutPoolRefused(t *testing.T) {
	_, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith("", "", "", "", "0x1"), nil, "", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--shadow-seed") {
		t.Fatalf("a seed with no pool must be refused, got %v", err)
	}
}

func TestDrawShadowSeatsWarnsMemberEqualsPrimary(t *testing.T) {
	var errb bytes.Buffer
	_, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith("", "localmodel-a:1,localmodel-b:1", "", "", "0x1"),
		map[string]string{advpool.RoleMutantGenerator: "localmodel-a:1"}, filepath.Join(t.TempDir(), "x.duckdb"), &errb)
	if err != nil || !strings.Contains(errb.String(), "localmodel-a:1") || !strings.Contains(errb.String(), "itself") {
		t.Fatalf("err=%v stderr=%q", err, errb.String())
	}
}

func TestDrawShadowSeatsStrictRegistryNamesMember(t *testing.T) {
	// The model registry's strict mode refuses any value that is not a
	// declared alias. Declare one alias inline so the fixture is
	// self-contained: see internal/models for the env var (models.EnvInline)
	// and the exact syntax that turns strict on, and copy it from an
	// existing strict-mode test (grep -rn "Strict" cmd/corral/*_test.go).
	t.Setenv(models.EnvInline, `{"strict": true, "fast": {"provider": "ollama", "model": "localmodel-a:1", "endpoint": "http://127.0.0.1:11434"}}`)
	t.Setenv(models.EnvFile, "")
	_, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith("", "fast,typo-model", "", "", "0x1"), nil, filepath.Join(t.TempDir(), "x.duckdb"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "typo-model") || !strings.Contains(err.Error(), "shadow-pool") {
		t.Fatalf("a strict-registry typo in a pool must be refused naming the member and flag, got %v", err)
	}
}

// THE DOORS. Each command must take the pool flags off its own command line
// and reach the draw (or, for doctor, the validation) — the helper being
// called is not the same as every door calling it. Each run is stopped by a
// refusal that comes AFTER the draw, so nothing is spent.
//
// surface: --shadow-pool
// surface: --shadow-writer-pool
// surface: --shadow-seed
func TestEveryDoorDrawsFromItsPoolFlags(t *testing.T) {
	t.Setenv("CORRALAI_BUGCATCH_DB", filepath.Join(t.TempDir(), "bc.duckdb"))
	pools := []string{"--shadow-pool", "localmodel-a:1,localmodel-b:1", "--shadow-writer-pool", "localmodel-c:1,localmodel-d:1", "--shadow-seed", "0x2a"}
	doors := []struct {
		name string
		run  func(args []string, stdout, stderr io.Writer) int
		args []string
	}{
		// No --code: refused right after the draw.
		{"certify --local", runCertifyLocal, append([]string{"--lang", "go"}, pools...)},
		// --scope-tests is refused right after the registry, which runs after the draw.
		{"certify --repo", runCertifyRepo, append([]string{"--repo", t.TempDir(), "--scope-tests"}, pools...)},
	}
	for _, d := range doors {
		t.Run(d.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if rc := d.run(d.args, &out, &errb); rc != 2 {
				t.Fatalf("rc = %d, want the usage refusal after the draw\n%s", rc, errb.String())
			}
			for _, want := range []string{"mutant-generator challenger drawn from a pool of 2", "test-writer challenger drawn from a pool of 2", "0x000000000000002a"} {
				if !strings.Contains(errb.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, errb.String())
				}
			}
		})
	}
}

// surface: --shadow-pool
// surface: --shadow-writer-pool
// surface: --shadow-writer-model
func TestDoctorValidatesShadowPools(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"pool of one", []string{"--shadow-pool", "localmodel-a:1"}, "at least two"},
		{"writer named and pooled", []string{"--shadow-writer-model", "localmodel-a:1", "--shadow-writer-pool", "localmodel-a:1,localmodel-b:1"}, "--shadow-writer-model and --shadow-writer-pool are both set"},
		{"seed with no pool", []string{"--shadow-seed", "0x1"}, "no draw to replay"},
		{"unparseable seed", []string{"--shadow-pool", "localmodel-a:1,localmodel-b:1", "--shadow-seed", "zz"}, "--shadow-seed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if rc := runDoctor(append([]string{"--repo", t.TempDir()}, c.args...), &out, &errb); rc == 0 {
				t.Fatalf("doctor passed a broken pool:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "[FAIL] shadow pool") || !strings.Contains(out.String(), c.want) {
				t.Fatalf("doctor did not report the pool, want %q:\n%s%s", c.want, out.String(), errb.String())
			}
		})
	}
	var out bytes.Buffer
	runDoctor([]string{"--repo", t.TempDir(), "--shadow-pool", "localmodel-a:1,localmodel-b:1"}, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "[ok  ] shadow pool") {
		t.Fatalf("a valid pool was not reported as checked:\n%s", out.String())
	}
}

// The verdict seat reaches the draw as TYPED — before the registry runs — so
// an alias on the primary must still be recognised as the member it names.
func TestDrawShadowSeatsWarnsMemberEqualsAliasedPrimary(t *testing.T) {
	t.Setenv(models.EnvInline, `{"edge": {"provider": "ollama", "model": "localmodel-a:1", "endpoint": "http://127.0.0.1:11434"}}`)
	t.Setenv(models.EnvFile, "")
	var errb bytes.Buffer
	_, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith("", "localmodel-a:1,localmodel-b:1", "", "", "0x1"),
		map[string]string{advpool.RoleMutantGenerator: "edge"}, filepath.Join(t.TempDir(), "x.duckdb"), &errb)
	if err != nil || !strings.Contains(errb.String(), "itself") {
		t.Fatalf("err=%v stderr=%q", err, errb.String())
	}
}

// history_rows is signed and printed as the evidence behind THIS draw, so it
// counts rows about the pool's members only. A primary's rows sit in the same
// roles the draw reads; counting them made two never-run members on a flat
// prior read "history 214 row(s)".
func TestDrawShadowSeatsHistoryRowsCountOnlyPoolMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bc.duckdb")
	s, err := bugcatch.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1, 0).UTC()
	if err := s.Record(context.Background(), []bugcatch.Observation{
		{TS: ts, RecordID: 1, Model: "primary-model:1", Role: "test-writer", Lang: "go", Catches: 3, Opportunities: 4},
		{TS: ts, RecordID: 2, Model: "primary-model:1", Role: "test-writer", Lang: "go", Catches: 3, Opportunities: 4},
		{TS: ts, RecordID: 3, Model: "localmodel-a:1", Role: "test-writer-shadow", Lang: "go", Catches: 1, Opportunities: 2, Shadow: true},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var errb bytes.Buffer
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith("", "", "", "localmodel-a:1,localmodel-b:1", "0x1"), nil, path, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if sels[0].HistoryRows != 1 {
		t.Fatalf("history_rows = %d, want 1 (the primary's 2 rows are not about any pool member)", sels[0].HistoryRows)
	}
	if !strings.Contains(errb.String(), "history 1 row(s) for go") {
		t.Fatalf("printed history must match the signed count: %q", errb.String())
	}
}

// The generator's history is SURVIVED over PLANTED — a challenger generator
// succeeds when its mutants survive. Draw clamps successes to trials, so a
// swapped mapping would not crash; it would quietly invert the posterior.
func TestDrawShadowSeatsGeneratorHistoryIsSurvivedOverPlanted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bc.duckdb")
	s, err := bugcatch.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1, 0).UTC()
	if err := s.Record(context.Background(), []bugcatch.Observation{
		{TS: ts, RecordID: 1, Model: "localmodel-a:1", Role: "mutant-generator", Lang: "go", MutantsPlanted: 10, MutantsSurvived: 3, Catches: 9, Opportunities: 9},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", flagsWith("", "localmodel-a:1,localmodel-b:1", "", "", "0x1"), nil, path, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if m := sels[0].Members[0]; m.Alpha != 4 || m.Beta != 8 {
		t.Fatalf("generator posterior α=%v β=%v, want 4,8 (1+3 survived, 1+7 killed of 10 planted)", m.Alpha, m.Beta)
	}
}

// A pooled draw (LangAny, the --repo scan) reads every RECORDED language and
// never a row whose language was never recorded. "any" is a label for the
// record, not a value in the store: passed through, it would query
// lang = 'any', find nothing, and sign a measured zero.
func TestDrawShadowSeatsPooledReadsEveryRecordedLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bc.duckdb")
	s, err := bugcatch.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1, 0).UTC()
	if err := s.Record(context.Background(), []bugcatch.Observation{
		{TS: ts, RecordID: 1, Model: "localmodel-a:1", Role: "test-writer", Lang: "go", Catches: 1, Opportunities: 2},
		{TS: ts, RecordID: 2, Model: "localmodel-a:1", Role: "test-writer", Lang: "python", Catches: 2, Opportunities: 2},
		{TS: ts, RecordID: 3, Model: "localmodel-a:1", Role: "test-writer", Catches: 50, Opportunities: 50}, // NULL lang
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	sels, err := drawShadowSeats("corral certify --repo", t.TempDir(), shadowpool.LangAny, flagsWith("", "", "", "localmodel-a:1,localmodel-b:1", "0x1"), nil, path, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	sel := sels[0]
	if sel.Lang != shadowpool.LangAny || sel.History != shadowpool.HistoryRead || sel.HistoryRows != 2 {
		t.Fatalf("pooled selection %+v, want lang any, read, 2 rows", sel)
	}
	if m := sel.Members[0]; m.Alpha != 4 || m.Beta != 2 {
		t.Fatalf("pooled posterior α=%v β=%v, want 4,2 (go + python, the NULL-lang row excluded)", m.Alpha, m.Beta)
	}
}
