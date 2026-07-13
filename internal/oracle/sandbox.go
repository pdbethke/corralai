// SPDX-License-Identifier: Elastic-2.0

package oracle

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pdbethke/corralai/internal/sqlguard"
)

// applyLockdown freezes a connection that ALREADY has the reporting schema present so it can
// read only that schema — no local files, no new attach, no URL, no extension autoload. MUST
// be applied to the SAME *sql.Conn the query later runs on (database/sql pools connections).
// Oracle's own RESOURCE caps (memory/threads/expression depth) are applied FIRST here, then
// the shared security lockdown from sqlguard closes with lock_configuration=true (must be last).
func applyLockdown(ctx context.Context, conn *sql.Conn) error {
	resourceCaps := []string{
		`SET memory_limit = '512MB'`,
		`SET threads = 2`,
		`SET max_expression_depth = 100`,
	}
	for _, s := range resourceCaps {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("lockdown %q: %w", s, err)
		}
	}
	return sqlguard.ApplyLockdown(ctx, conn)
}

// runLocked validates then executes userSQL on the already-locked conn, scanning at most
// rowCap rows (the read cap is bulletproof regardless of what the query produces).
func runLocked(ctx context.Context, conn *sql.Conn, userSQL string, rowCap int) ([]string, [][]string, error) {
	if err := sqlguard.ValidateReadOnly(userSQL); err != nil {
		return nil, nil, err
	}
	rs, err := conn.QueryContext(ctx, userSQL)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	var rows [][]string
	for rs.Next() && len(rows) < rowCap {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(cols))
		for i, v := range raw {
			row[i] = fmt.Sprintf("%v", v)
		}
		rows = append(rows, row)
	}
	return cols, rows, rs.Err()
}
