// SPDX-License-Identifier: Elastic-2.0

// internal/oracle/oracle_test.go
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/pdbethke/corralai/internal/fleet"
)

// fakeLLM is a scripted LLM: returns answers[call] in call order.
// When a call's system prompt contains "analyst" (the narrator), the user
// string is appended to narrateUsers so tests can inspect what the narrator saw.
type fakeLLM struct {
	answers      []string
	calls        int
	narrateUsers []string
	// delay, if non-zero, is injected before each answer to simulate a slow LLM.
	delay time.Duration
}

func (f *fakeLLM) Ask(_ context.Context, system, user string) (string, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if strings.Contains(system, "analyst") {
		f.narrateUsers = append(f.narrateUsers, user)
	}
	i := f.calls
	f.calls++
	if i >= len(f.answers) {
		return "", fmt.Errorf("fakeLLM: unexpected call %d (only %d scripted)", i, len(f.answers))
	}
	return f.answers[i], nil
}

// testConnectFn returns a connect func that builds an in-mem DuckDB seeded with
// the given mission rows, creates fleet_missions_current, and applies the lockdown.
// This mirrors the production connect (attach → views → lockdown on the same conn)
// without any md: network calls.
//
// missions: each entry is [brain, id_str, status] — the curated subset used by tests.
func testConnectFn(t *testing.T, missions [][3]string) func(ctx context.Context) (*sql.Conn, func(), error) {
	t.Helper()
	return func(ctx context.Context) (*sql.Conn, func(), error) {
		db, err := sql.Open("duckdb", "")
		if err != nil {
			return nil, nil, err
		}
		db.SetMaxOpenConns(1)
		conn, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			return nil, nil, err
		}
		closer := func() { conn.Close(); db.Close() }

		// Create the remote schema + all fleet tables (stands in for md: ATTACH).
		// DDL mirrors the fleet tableSpecs so all advertised names resolve, including
		// the append tables (fleet_actions, fleet_tasks, fleet_telemetry).
		setup := []string{
			`CREATE SCHEMA remote`,
			`CREATE TABLE remote.fleet_missions (
				brain        VARCHAR,
				id           BIGINT,
				directive    VARCHAR,
				status       VARCHAR,
				repo         VARCHAR,
				branch       VARCHAR,
				pr_url       VARCHAR,
				review_rounds BIGINT,
				created_ts   DOUBLE,
				updated_ts   DOUBLE
			)`,
			`CREATE TABLE remote.fleet_actions (
				brain   VARCHAR,
				id      BIGINT,
				ts      DOUBLE,
				agent   VARCHAR,
				action  VARCHAR
			)`,
			`CREATE TABLE remote.fleet_tasks (
				brain       VARCHAR,
				id          BIGINT,
				mission_id  BIGINT,
				key         VARCHAR,
				role        VARCHAR,
				title       VARCHAR,
				status      VARCHAR,
				claimed_by  VARCHAR,
				created_ts  DOUBLE,
				done_ts     DOUBLE
			)`,
			`CREATE TABLE remote.fleet_telemetry (
				brain      VARCHAR,
				id         BIGINT,
				ts         DOUBLE,
				mission_id BIGINT,
				kind       VARCHAR,
				actor      VARCHAR,
				subject    VARCHAR
			)`,
		}
		for _, s := range setup {
			if _, err := conn.ExecContext(ctx, s); err != nil {
				closer()
				return nil, nil, fmt.Errorf("test setup: %w", err)
			}
		}

		// Seed mission rows.
		for i, m := range missions {
			brain := strings.ReplaceAll(m[0], "'", "''")
			id := m[1]
			status := strings.ReplaceAll(m[2], "'", "''")
			q := fmt.Sprintf(
				`INSERT INTO remote.fleet_missions VALUES ('%s', %s, 'test directive', '%s', '', '', '', 0, 0.0, %d.0)`,
				brain, id, status, i+1,
			)
			if _, err := conn.ExecContext(ctx, q); err != nil {
				closer()
				return nil, nil, fmt.Errorf("test seed row %d: %w", i, err)
			}
		}

		// Create the current-state views — mirrors production (after attach, before lockdown).
		for _, v := range fleet.CurrentStateViews() {
			if _, err := conn.ExecContext(ctx, v); err != nil {
				closer()
				return nil, nil, fmt.Errorf("test views: %w", err)
			}
		}

		// Lockdown — must be the same conn as the view creation and future queries.
		if err := applyLockdown(ctx, conn); err != nil {
			closer()
			return nil, nil, fmt.Errorf("test lockdown: %w", err)
		}
		return conn, closer, nil
	}
}

// makeTestClient wires a Client with the in-mem connect seam.
func makeTestClient(t *testing.T, llm *fakeLLM, missions [][3]string, opts Options) *Client {
	t.Helper()
	c := New("md:test", llm, opts)
	c.connect = testConnectFn(t, missions)
	return c
}

// Case 1: happy path — first SQL attempt succeeds, narration returned, rows reflect seed.
func TestOracleHappyPath(t *testing.T) {
	missions := [][3]string{
		{"A", "1", "running"},
		{"A", "2", "running"},
		{"A", "3", "done"},
	}
	const validSQL = `SELECT status, count(*) AS c FROM fleet_missions_current GROUP BY status ORDER BY status`
	llm := &fakeLLM{answers: []string{validSQL, "2 running, 1 done"}}
	c := makeTestClient(t, llm, missions, Options{})

	ans, err := c.Ask(context.Background(), "how many missions per status?")
	if err != nil {
		t.Fatalf("happy path error: %v", err)
	}
	if ans.SQL != validSQL {
		t.Errorf("SQL mismatch: got %q", ans.SQL)
	}
	if ans.Narration != "2 running, 1 done" {
		t.Errorf("narration mismatch: got %q", ans.Narration)
	}
	if len(ans.Rows) != 2 {
		t.Errorf("expected 2 result rows (done + running), got %d", len(ans.Rows))
	}
	// Narration LLM must have been called with ≤ NarrateK rows.
	if len(llm.narrateUsers) != 1 {
		t.Fatalf("expected 1 narrate call, got %d", len(llm.narrateUsers))
	}
}

// Case 2: retry recovery — first SQL is broken, second attempt produces a valid SELECT.
func TestOracleRetryRecovery(t *testing.T) {
	missions := [][3]string{{"A", "1", "running"}}
	const validSQL = `SELECT status FROM fleet_missions_current`
	llm := &fakeLLM{answers: []string{
		"SELCT broken query here", // attempt 0: malformed, fails validateSelect
		validSQL,                  // attempt 1: valid SELECT
		"1 running mission",       // narration
	}}
	c := makeTestClient(t, llm, missions, Options{MaxRetries: 2})

	ans, err := c.Ask(context.Background(), "what missions are running?")
	if err != nil {
		t.Fatalf("retry recovery error: %v", err)
	}
	if ans.SQL != validSQL {
		t.Errorf("SQL after retry mismatch: got %q", ans.SQL)
	}
	if ans.Narration != "1 running mission" {
		t.Errorf("narration mismatch: got %q", ans.Narration)
	}
	if llm.calls != 3 {
		t.Errorf("expected 3 LLM calls (2 SQL + 1 narrate), got %d", llm.calls)
	}
}

// Case 3: validateSelect gate — model keeps returning a non-SELECT after all retries;
// Ask must return an error and never execute the forbidden statement.
func TestOracleValidateSelectGate(t *testing.T) {
	missions := [][3]string{{"A", "1", "running"}}
	// Provide MaxRetries+1 = 3 answers so generateSQL never runs out.
	llm := &fakeLLM{answers: []string{
		"DROP TABLE fleet_missions",
		"DROP TABLE fleet_missions",
		"DROP TABLE fleet_missions",
	}}
	c := makeTestClient(t, llm, missions, Options{MaxRetries: 2})

	ans, err := c.Ask(context.Background(), "delete everything")
	if err == nil {
		t.Fatal("expected error for non-SELECT across all retries, got nil")
	}
	if len(ans.Rows) > 0 {
		t.Error("no rows should be returned when all retries are refused by validateSelect")
	}
	// Error should communicate that the query was rejected (not a network / runtime error).
	if !strings.Contains(err.Error(), "valid") && !strings.Contains(err.Error(), "SELECT") {
		t.Errorf("error should reference query validity/SELECT, got: %v", err)
	}
	// Narrator must NOT have been called (no execution, no rows to narrate).
	if len(llm.narrateUsers) != 0 {
		t.Errorf("narrator should not be called when all attempts are refused; got %d narrate call(s)", len(llm.narrateUsers))
	}
}

// Case 4: narration cap — seed 200 rows, NarrateK=50; narrator receives ≤ 50 rows
// while Answer.Rows holds all rows up to RowCap.
func TestOracleNarrationCap(t *testing.T) {
	const N = 200
	missions := make([][3]string, N)
	for i := range missions {
		missions[i] = [3]string{"A", fmt.Sprintf("%d", i+1), "running"}
	}
	const validSQL = `SELECT brain, status FROM fleet_missions_current`
	llm := &fakeLLM{answers: []string{validSQL, "200 running missions"}}
	c := makeTestClient(t, llm, missions, Options{RowCap: 1000, NarrateK: 50})

	ans, err := c.Ask(context.Background(), "list all missions")
	if err != nil {
		t.Fatalf("narration cap error: %v", err)
	}
	if len(ans.Rows) != N {
		t.Errorf("Answer.Rows: expected %d (all seeded rows ≤ RowCap=1000), got %d", N, len(ans.Rows))
	}

	// Inspect what the narrator received via the recorded user message.
	if len(llm.narrateUsers) != 1 {
		t.Fatalf("expected 1 narrate call, got %d", len(llm.narrateUsers))
	}
	narrateUser := llm.narrateUsers[0]

	// The result table starts after "Result:\n"; split into lines.
	const marker = "Result:\n"
	resultIdx := strings.Index(narrateUser, marker)
	if resultIdx < 0 {
		t.Fatalf("narrator user message missing %q", marker)
	}
	resultSection := strings.TrimSpace(narrateUser[resultIdx+len(marker):])
	lines := strings.Split(resultSection, "\n")
	// lines[0] = column header; remaining lines = data rows.
	dataRowCount := len(lines) - 1
	if dataRowCount > 50 {
		t.Errorf("narrator was given %d data rows; expected ≤ NarrateK=50", dataRowCount)
	}
	// Also verify the "(showing the first 50 of 200 rows)" note is present.
	if !strings.Contains(narrateUser, "showing the first 50") {
		t.Errorf("narrator user msg should include truncation note; got: %q", narrateUser[:min(200, len(narrateUser))])
	}
}

// Case 5: disabled — Enabled()==false and Ask returns "unavailable" for both
// empty-target and nil-llm configurations.
func TestOracleDisabled(t *testing.T) {
	llm := &fakeLLM{answers: []string{"SELECT 1"}}

	t.Run("empty_target", func(t *testing.T) {
		c := New("", llm, Options{})
		if c.Enabled() {
			t.Error("Enabled() should be false when mdTarget is empty")
		}
		_, err := c.Ask(context.Background(), "anything")
		if err == nil {
			t.Fatal("Ask on disabled oracle should return an error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unavailable") {
			t.Errorf("error should mention 'unavailable', got: %v", err)
		}
		if llm.calls != 0 {
			t.Errorf("disabled oracle must not call the LLM; calls=%d", llm.calls)
		}
	})

	t.Run("nil_llm", func(t *testing.T) {
		c := New("md:test", nil, Options{})
		if c.Enabled() {
			t.Error("Enabled() should be false when llm is nil")
		}
		_, err := c.Ask(context.Background(), "anything")
		if err == nil {
			t.Fatal("Ask on nil-llm oracle should return an error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unavailable") {
			t.Errorf("error should mention 'unavailable', got: %v", err)
		}
	})
}

// Case 6 (Bug 1 end-to-end proof): the LLM returns a query against an append table
// using the qualified name advertised by ReportingSchema (remote.fleet_actions).
// This proves the connect seam exposes the remote catalog correctly AND the schema
// advertises the right prefix. It FAILS before the schema/seed fix (remote.fleet_actions
// did not exist in the old connect seam → catalog error → all retries fail) and PASSES after.
func TestOracleAppendTableQuery(t *testing.T) {
	// testConnectFn now seeds remote.fleet_actions; we use makeTestClient (missions=nil).
	// We manually seed fleet_actions rows via an augmented connect fn.
	const actionSQL = `SELECT count(*) AS n FROM remote.fleet_actions`
	llm := &fakeLLM{answers: []string{actionSQL, "3 actions recorded"}}

	// Build a connect fn that seeds 3 fleet_actions rows in addition to the standard setup.
	connectFn := func(ctx context.Context) (*sql.Conn, func(), error) {
		base := testConnectFn(t, nil) // no missions needed
		conn, closer, err := base(ctx)
		if err != nil {
			return nil, nil, err
		}
		for i := 1; i <= 3; i++ {
			q := fmt.Sprintf(
				`INSERT INTO remote.fleet_actions VALUES ('brain-A', %d, %d.0, 'agent-1', 'action-%d')`,
				i, i, i,
			)
			if _, err := conn.ExecContext(ctx, q); err != nil {
				closer()
				return nil, nil, fmt.Errorf("seed fleet_actions row %d: %w", i, err)
			}
		}
		return conn, closer, nil
	}

	c := New("md:test", llm, Options{MaxRetries: 0})
	c.connect = connectFn

	ans, err := c.Ask(context.Background(), "how many actions are recorded?")
	if err != nil {
		t.Fatalf("end-to-end append-table query now resolves: unexpected error: %v", err)
	}
	if len(ans.Rows) != 1 {
		t.Fatalf("expected 1 result row (count), got %d", len(ans.Rows))
	}
	if ans.Rows[0][0] != "3" {
		t.Errorf("expected count=3 (seeded actions), got %q", ans.Rows[0][0])
	}
}

// Case 7 (Bug 2 proof): a slow LLM (each call sleeps longer than Timeout) must NOT
// be killed by the per-statement Timeout. Only runLocked is bounded by Timeout; the
// LLM calls use the OverallTimeout context. This test fails under the old code where
// Timeout wrapped the entire pipeline, and passes after the fix.
func TestSlowLLMNotKilledByStatementTimeout(t *testing.T) {
	missions := [][3]string{{"A", "1", "running"}}
	const validSQL = `SELECT status FROM fleet_missions_current`

	// Each LLM call sleeps 80ms. With Timeout=50ms (statement-only), the LLM sleep
	// exceeds the statement timeout, but that timeout only covers runLocked now.
	// OverallTimeout=5s gives the full pipeline (2 LLM calls × 80ms + fast query) plenty of room.
	llm := &fakeLLM{
		answers: []string{validSQL, "1 running mission"},
		delay:   80 * time.Millisecond,
	}
	c := makeTestClient(t, llm, missions, Options{
		Timeout:        50 * time.Millisecond, // per-statement budget; shorter than LLM delay
		OverallTimeout: 5 * time.Second,       // overall budget; comfortably covers all calls
		MaxRetries:     0,
	})

	ans, err := c.Ask(context.Background(), "what's running?")
	if err != nil {
		t.Fatalf("slow LLM should not be killed by the per-statement timeout: %v", err)
	}
	if len(ans.Rows) != 1 {
		t.Errorf("expected 1 result row, got %d", len(ans.Rows))
	}
}
