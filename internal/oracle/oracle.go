// SPDX-License-Identifier: Elastic-2.0

package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/pdbethke/corralai/internal/fleet"
)

// LLM is satisfied by *llm.Client (the brain's narrator) and by test fakes.
type LLM interface {
	Ask(ctx context.Context, system, user string) (string, error)
}

// Answer is the structured result of a natural-language oracle query.
type Answer struct {
	Narration string     `json:"narration"`
	SQL       string     `json:"sql"`
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
}

// Options configures the oracle pipeline. Zero values use the defaults below.
type Options struct {
	RowCap, NarrateK, MaxRetries int
	// Timeout is the per-statement (runLocked) budget. It cancels a slow DuckDB/MotherDuck
	// query without killing the surrounding LLM calls. Default 30s (generous for md: latency).
	Timeout time.Duration
	// OverallTimeout caps the entire Ask call — all LLM round-trips + retries + query + narrate.
	// This ensures caller-unbounded contexts (e.g. MCP server) can't hang forever. Default 120s.
	OverallTimeout time.Duration
}

// Client runs the NL→SQL→narrate pipeline over a pinned, locked DuckDB connection.
type Client struct {
	target string
	llm    LLM
	opts   Options
	// connect yields a pinned, reporting-schema-loaded, locked connection + a closer.
	// Defaulted to connectMotherDuck; overridden in tests to an in-mem seed.
	connect func(ctx context.Context) (*sql.Conn, func(), error)
}

// New creates an oracle Client. Returns a client with Enabled()==false when
// mdTarget is empty or llm is nil — Ask is safe to call on such a client.
func New(mdTarget string, llm LLM, opts Options) *Client {
	if opts.RowCap == 0 {
		opts.RowCap = 1000
	}
	if opts.NarrateK == 0 {
		opts.NarrateK = 50
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 2
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second // per-statement; bumped from 5s to tolerate md: network latency
	}
	if opts.OverallTimeout == 0 {
		opts.OverallTimeout = 120 * time.Second // covers up to MaxRetries LLM calls + query + narrate
	}
	c := &Client{target: mdTarget, llm: llm, opts: opts}
	c.connect = c.connectMotherDuck
	return c
}

// Enabled reports whether the oracle is configured and safe to call.
func (c *Client) Enabled() bool { return c != nil && c.target != "" && c.llm != nil }

// connectMotherDuck is the production connect: opens an in-mem DuckDB, attaches
// the MotherDuck target READ_ONLY, creates the current-state views, then applies
// the lockdown — all on ONE pinned conn so lockdown and queries share it.
func (c *Client) connectMotherDuck(ctx context.Context) (*sql.Conn, func(), error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1) // pin one conn so lockdown + query share it
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	closer := func() { _ = conn.Close(); _ = db.Close() }

	// Install/load MotherDuck extension then attach the target read-only.
	for _, s := range []string{
		"INSTALL motherduck; LOAD motherduck;",
		fmt.Sprintf("ATTACH '%s' AS remote (READ_ONLY)", strings.ReplaceAll(c.target, "'", "''")),
	} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			closer()
			return nil, nil, fmt.Errorf("oracle connect: %w", err)
		}
	}
	// Create current-state views before the lockdown freezes the session.
	for _, v := range fleet.CurrentStateViews() {
		if _, err := conn.ExecContext(ctx, v); err != nil {
			closer()
			return nil, nil, fmt.Errorf("oracle views: %w", err)
		}
	}
	// Lockdown AFTER attach + views — must be the same conn.
	if err := applyLockdown(ctx, conn); err != nil {
		closer()
		return nil, nil, err
	}
	return conn, closer, nil
}

// Ask translates question into SQL via the LLM, executes it on the locked
// connection, then narrates the result. Returns an error if the oracle is
// disabled, the query can't be validated after MaxRetries, or execution fails.
func (c *Client) Ask(ctx context.Context, question string) (Answer, error) {
	if !c.Enabled() {
		return Answer{}, fmt.Errorf("fleet oracle unavailable (configure CORRALAI_MOTHERDUCK + a model backend)")
	}
	if r := []rune(question); len(r) > 4000 {
		question = string(r[:4000]) // rune-safe: never split a multi-byte UTF-8 char
	}
	// Wrap the entire Ask pipeline in the overall budget so caller-unbounded contexts
	// (e.g. the MCP server's per-request ctx) can't hang through multiple LLM round-trips.
	ctx, cancel := context.WithTimeout(ctx, c.opts.OverallTimeout)
	defer cancel()

	conn, closer, err := c.connect(ctx)
	if err != nil {
		return Answer{}, err
	}
	defer closer()

	schema := fleet.ReportingSchema()
	var lastErr string
	var sqlText string
	var cols []string
	var rows [][]string

	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		sqlText, err = c.generateSQL(ctx, question, schema, lastErr)
		if err != nil {
			return Answer{}, err
		}
		// Validation runs inside runLocked (the security-critical single chokepoint).
		// Errors — including validation rejections — are captured in lastErr so the
		// loop retries with the error fed back to the LLM.
		qctx, qcancel := context.WithTimeout(ctx, c.opts.Timeout)
		cols, rows, err = runLocked(qctx, conn, sqlText, c.opts.RowCap)
		qcancel()
		if err != nil {
			lastErr = err.Error()
			continue
		}
		lastErr = ""
		break
	}
	if lastErr != "" {
		return Answer{SQL: sqlText}, fmt.Errorf("could not produce a valid query: %s", lastErr)
	}

	narration, nerr := c.narrate(ctx, question, cols, rows)
	if nerr != nil {
		// Narration is best-effort; a timeout/LLM failure still leaves useful SQL+Rows.
		log.Printf("oracle narrate: %v", nerr)
	}
	return Answer{Narration: narration, SQL: sqlText, Columns: cols, Rows: rows}, nil
}

// generateSQL calls the LLM to produce a single SELECT/WITH for the question.
// When priorErr is non-empty it is included as error-feedback so the model can retry.
func (c *Client) generateSQL(ctx context.Context, question, schema, priorErr string) (string, error) {
	system := "You translate a question into ONE read-only DuckDB SELECT over the given fleet reporting schema. " +
		"Output ONLY the SQL, no prose, no backticks. Use fleet_missions_current for current mission state. " +
		"Never write anything but a single SELECT/WITH."
	user := "Schema:\n" + schema + "\n\nQuestion: " + question
	if priorErr != "" {
		user += "\n\nYour previous query failed with: " + priorErr + "\nFix it and output only the corrected SELECT."
	}
	out, err := c.llm.Ask(ctx, system, user)
	if err != nil {
		return "", err
	}
	return cleanSQL(out), nil
}

// cleanSQL strips markdown code fences and stray backticks the model may emit.
// The opening fence tag is matched case-insensitively (```sql / ```SQL / ```Sql).
func cleanSQL(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		// Strip a leading language tag (sql/SQL/…) if present, case-insensitively.
		if len(s) >= 3 && strings.EqualFold(s[:3], "sql") {
			s = s[3:]
		}
		s = strings.TrimSpace(s)
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// narrate feeds the top-NarrateK rows to the LLM for a 1-3 sentence summary.
// rows beyond NarrateK are capped here; Answer.Rows still holds up to RowCap rows.
func (c *Client) narrate(ctx context.Context, question string, cols []string, rows [][]string) (string, error) {
	k := c.opts.NarrateK
	shown := rows
	note := ""
	if len(rows) > k {
		shown = rows[:k]
		note = fmt.Sprintf(" (showing the first %d of %d rows)", k, len(rows))
	}
	var b strings.Builder
	b.WriteString(strings.Join(cols, " | ") + "\n")
	for _, r := range shown {
		b.WriteString(strings.Join(r, " | ") + "\n")
	}
	system := "You are the fleet's analyst. Answer the question in 1-3 sentences from the result rows. " +
		"Be precise; if the result is empty, say nothing in the fleet matches."
	user := "Question: " + question + note + "\n\nResult:\n" + b.String()
	return c.llm.Ask(ctx, system, user)
}
