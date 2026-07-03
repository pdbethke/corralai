// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/fleet"
	"github.com/pdbethke/corralai/internal/oracle"
)

// fleetFakeLLM is a scripted LLM for ask_fleet tests. calls is mutex-guarded so a
// future t.Parallel() or concurrent server access can't race the counter.
type fleetFakeLLM struct {
	answers []string
	mu      sync.Mutex
	calls   int
}

func (f *fleetFakeLLM) Ask(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.mu.Unlock()
	if i >= len(f.answers) {
		return "", fmt.Errorf("fleetFakeLLM: unexpected call %d (only %d scripted)", i, len(f.answers))
	}
	return f.answers[i], nil
}

// callCount returns the current call count under the lock.
func (f *fleetFakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fleetTestConnectFn returns an oracle connect function backed by an in-mem DuckDB
// seeded with the given missions. Mirrors oracle's testConnectFn but without
// applyLockdown (unexported from oracle; security-only, not functionally required
// for unit-test queries).
func fleetTestConnectFn(t *testing.T, missions [][3]string) func(context.Context) (*sql.Conn, func(), error) {
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
		}
		for _, s := range setup {
			if _, err := conn.ExecContext(ctx, s); err != nil {
				closer()
				return nil, nil, fmt.Errorf("brain/test setup: %w", err)
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
				return nil, nil, fmt.Errorf("brain/test seed row %d: %w", i, err)
			}
		}
		for _, v := range fleet.CurrentStateViews() {
			if _, err := conn.ExecContext(ctx, v); err != nil {
				closer()
				return nil, nil, fmt.Errorf("brain/test views: %w", err)
			}
		}
		return conn, closer, nil
	}
}

// buildFleetOracle wires an oracle.Client with an in-mem connect seam via the
// test-only constructor (NewForTest bypasses the md: lockdown path — tests only).
func buildFleetOracle(t *testing.T, llm *fleetFakeLLM, missions [][3]string) *oracle.Client {
	t.Helper()
	return oracle.NewForTest("md:test", llm, oracle.Options{}, fleetTestConnectFn(t, missions))
}

// newFleetBrain starts a brain with Options{Oracle: c, AskFleetRateLimit: limit}
// over an in-memory MCP transport and returns the connected session.
func newFleetBrain(t *testing.T, c *oracle.Client, rateLimit int) *mcp.ClientSession {
	t.Helper()
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	opts := Options{Oracle: c, AskFleetRateLimit: rateLimit}
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "fleet-test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// contentText extracts plain text from the first TextContent block in a tool result.
// Used to inspect error messages on IsError results.
func contentText(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	b, err := res.Content[0].MarshalJSON()
	if err != nil {
		return ""
	}
	var tc struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &tc); err != nil {
		return ""
	}
	return tc.Text
}

// callAskFleet calls ask_fleet and returns the result (does NOT fatal on tool errors).
func callAskFleet(t *testing.T, sess *mcp.ClientSession, name, question string) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "ask_fleet",
		Arguments: map[string]any{"name": name, "question": question},
	})
	if err != nil {
		t.Fatalf("ask_fleet transport error: %v", err)
	}
	return res
}

// TestAskFleetHappyPath: oracle returns narration + rows; tool surfaces them.
func TestAskFleetHappyPath(t *testing.T) {
	missions := [][3]string{
		{"A", "1", "running"},
		{"A", "2", "done"},
	}
	const validSQL = `SELECT status, count(*) AS c FROM fleet_missions_current GROUP BY status ORDER BY status`
	llm := &fleetFakeLLM{answers: []string{validSQL, "1 done, 1 running"}}
	c := buildFleetOracle(t, llm, missions)
	sess := newFleetBrain(t, c, 0) // 0 => default (10)

	res := callAskFleet(t, sess, "Alice", "how many missions per status?")
	if res.IsError {
		t.Fatalf("expected success, got tool error: %+v", res.Content)
	}

	var ans oracle.Answer
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &ans); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if ans.Narration != "1 done, 1 running" {
		t.Errorf("narration mismatch: got %q", ans.Narration)
	}
	if ans.SQL != validSQL {
		t.Errorf("SQL mismatch: got %q", ans.SQL)
	}
	if len(ans.Rows) != 2 {
		t.Errorf("expected 2 result rows, got %d", len(ans.Rows))
	}
	if n := llm.callCount(); n != 2 {
		t.Errorf("expected 2 LLM calls (SQL + narrate), got %d", n)
	}
}

// TestAskFleetRateLimit: rate limit trips before oracle runs after N calls.
func TestAskFleetRateLimit(t *testing.T) {
	missions := [][3]string{{"A", "1", "running"}}
	const validSQL = `SELECT status FROM fleet_missions_current`
	// Provide enough scripted answers for rateLimit successful calls (each = SQL + narrate = 2).
	const rateLimit = 3
	answers := make([]string, 0, rateLimit*2)
	for i := 0; i < rateLimit; i++ {
		answers = append(answers, validSQL, "1 running")
	}
	llm := &fleetFakeLLM{answers: answers}
	c := buildFleetOracle(t, llm, missions)
	sess := newFleetBrain(t, c, rateLimit)

	// First rateLimit calls must succeed.
	for i := 0; i < rateLimit; i++ {
		res := callAskFleet(t, sess, "Bob", "what's running?")
		if res.IsError {
			t.Fatalf("call %d should succeed (within rate limit=%d): %+v", i+1, rateLimit, res.Content)
		}
	}

	// The (rateLimit+1)-th call must be refused; the oracle must NOT be called.
	llmCallsBefore := llm.callCount()
	res := callAskFleet(t, sess, "Bob", "what's running?")
	if !res.IsError {
		t.Fatal("expected rate-limit error on call over limit, got success")
	}
	// Verify error message mentions rate limit — extract text from the content slice.
	errText := contentText(res)
	if !strings.Contains(strings.ToLower(errText), "rate limit") {
		t.Errorf("error should mention 'rate limit', got: %s", errText)
	}
	// Oracle must not have been called on the over-limit attempt.
	if n := llm.callCount(); n != llmCallsBefore {
		t.Errorf("oracle/LLM must not be called on over-limit request; llm.calls went from %d to %d", llmCallsBefore, n)
	}
}

// TestAskFleetUnregisteredWhenOracleNil: ask_fleet must not appear in the tool list
// when Options.Oracle is nil.
func TestAskFleetUnregisteredWhenOracleNil(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(store, nil, Options{}).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "nil-oracle-test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	lt, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range lt.Tools {
		if tool.Name == "ask_fleet" {
			t.Error("ask_fleet must NOT be registered when Options.Oracle is nil")
		}
	}
}

// TestAskFleetRegistrationDoesNotCallAsk: registering ask_fleet must not trigger
// any oracle Ask call (the oracle is lazy-connect; startup must not hit DuckDB/LLM).
func TestAskFleetRegistrationDoesNotCallAsk(t *testing.T) {
	// A fakeLLM with NO scripted answers — any call panics the test.
	llm := &fleetFakeLLM{answers: nil}
	c := buildFleetOracle(t, llm, nil)

	// Just build and connect; don't call ask_fleet.
	sess := newFleetBrain(t, c, 0)

	// Listing tools must not trigger Ask.
	if _, err := sess.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if n := llm.callCount(); n != 0 {
		t.Errorf("registration/listing must not call the oracle LLM; got %d call(s)", n)
	}
}
