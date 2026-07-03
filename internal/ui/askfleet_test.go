// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/pdbethke/corralai/internal/fleet"
	"github.com/pdbethke/corralai/internal/oracle"
)

// uiFakeLLM is a minimal scripted LLM for UI-layer oracle tests.
type uiFakeLLM struct {
	answers []string
	calls   int
}

func (f *uiFakeLLM) Ask(_ context.Context, _, _ string) (string, error) {
	i := f.calls
	f.calls++
	if i >= len(f.answers) {
		return "", fmt.Errorf("uiFakeLLM: unexpected call %d (only %d scripted)", i, len(f.answers))
	}
	return f.answers[i], nil
}

// uiTestConnectFn builds an in-mem DuckDB seeded with the given missions for
// UI oracle handler tests. Mirrors the brain-layer fleetTestConnectFn: applyLockdown
// is unexported from the oracle package (security-only, not functionally required
// for handler unit tests) so it is intentionally skipped here.
func uiTestConnectFn(t *testing.T, missions [][3]string) func(context.Context) (*sql.Conn, func(), error) {
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

		ddl := []string{
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
		}
		for _, s := range ddl {
			if _, err := conn.ExecContext(ctx, s); err != nil {
				closer()
				return nil, nil, fmt.Errorf("ui/test setup: %w", err)
			}
		}
		for i, m := range missions {
			brain := strings.ReplaceAll(m[0], "'", "''")
			id := m[1]
			status := strings.ReplaceAll(m[2], "'", "''")
			q := fmt.Sprintf(
				`INSERT INTO remote.fleet_missions VALUES ('%s', %s, 'directive', '%s', '', '', '', 0, 0.0, %d.0)`,
				brain, id, status, i+1,
			)
			if _, err := conn.ExecContext(ctx, q); err != nil {
				closer()
				return nil, nil, fmt.Errorf("ui/test seed row %d: %w", i, err)
			}
		}
		for _, v := range fleet.CurrentStateViews() {
			if _, err := conn.ExecContext(ctx, v); err != nil {
				closer()
				return nil, nil, fmt.Errorf("ui/test views: %w", err)
			}
		}
		return conn, closer, nil
	}
}

func buildTestOracle(t *testing.T, llm *uiFakeLLM, missions [][3]string) *oracle.Client {
	t.Helper()
	return oracle.NewForTest("md:test", llm, oracle.Options{}, uiTestConnectFn(t, missions))
}

// TestAskFleetHandler200: oracle wired with seeded missions + scripted LLM → 200 with narration, rows, SQL.
func TestAskFleetHandler200(t *testing.T) {
	missions := [][3]string{
		{"A", "1", "running"},
		{"A", "2", "done"},
	}
	const validSQL = `SELECT status, count(*) AS c FROM fleet_missions_current GROUP BY status ORDER BY status`
	llm := &uiFakeLLM{answers: []string{validSQL, "1 done, 1 running"}}
	oc := buildTestOracle(t, llm, missions)

	h := Handler(Deps{Oracle: oc})
	reqBody, _ := json.Marshal(map[string]string{"question": "how many missions per status?"})
	req := httptest.NewRequest(http.MethodPost, "/api/ask_fleet", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var ans oracle.Answer
	if err := json.NewDecoder(w.Body).Decode(&ans); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ans.Narration != "1 done, 1 running" {
		t.Errorf("narration mismatch: got %q", ans.Narration)
	}
	if ans.SQL != validSQL {
		t.Errorf("SQL mismatch: got %q", ans.SQL)
	}
	if len(ans.Rows) != 2 {
		t.Errorf("expected 2 result rows (done + running), got %d", len(ans.Rows))
	}
}

// TestAskFleetHandler503WhenNil: Oracle=nil → 503.
func TestAskFleetHandler503WhenNil(t *testing.T) {
	h := Handler(Deps{Oracle: nil})
	reqBody, _ := json.Marshal(map[string]string{"question": "any question"})
	req := httptest.NewRequest(http.MethodPost, "/api/ask_fleet", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when oracle is nil, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unavailable") {
		t.Errorf("503 body should say 'unavailable', got: %s", w.Body.String())
	}
}

// TestAskFleetHandler503WhenDisabled: oracle.New("", nil, …).Enabled()==false → 503.
func TestAskFleetHandler503WhenDisabled(t *testing.T) {
	// Empty mdTarget → Enabled()==false.
	disabled := oracle.NewForTest("", &uiFakeLLM{answers: nil}, oracle.Options{}, nil)
	h := Handler(Deps{Oracle: disabled})
	reqBody, _ := json.Marshal(map[string]string{"question": "any question"})
	req := httptest.NewRequest(http.MethodPost, "/api/ask_fleet", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when oracle is disabled, got %d: %s", w.Code, w.Body.String())
	}
}
