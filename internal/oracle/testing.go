// SPDX-License-Identifier: Elastic-2.0

package oracle

import (
	"context"
	"database/sql"
)

// NewForTest constructs a Client with a caller-supplied connect for tests ONLY —
// it bypasses the md: lockdown path and must never be used in production. It
// applies the same option defaults as New but pins the connect seam to connectFn
// instead of connectMotherDuck, so tests can drive the NL→SQL→narrate pipeline
// over an in-mem DuckDB with no network.
func NewForTest(mdTarget string, llm LLM, opts Options, connectFn func(context.Context) (*sql.Conn, func(), error)) *Client {
	c := New(mdTarget, llm, opts)
	c.connect = connectFn
	return c
}
