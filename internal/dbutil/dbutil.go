// SPDX-License-Identifier: Elastic-2.0

package dbutil

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"

	_ "github.com/lib/pq"
)

// ActiveDriver returns "postgres" if CORRALAI_POSTGRES_URL is set, otherwise "sqlite".
func ActiveDriver() string {
	if os.Getenv("CORRALAI_POSTGRES_URL") != "" {
		return "postgres"
	}
	return "sqlite"
}

// OpenDB opens a database connection based on the active driver environment variables.
// If defaultPath is supplied, it's used for SQLite when CORRALAI_POSTGRES_URL is unset.
func OpenDB(defaultPath string) (*sql.DB, error) {
	if url := os.Getenv("CORRALAI_POSTGRES_URL"); url != "" {
		db, err := sql.Open("postgres", url)
		if err != nil {
			return nil, err
		}
		return db, nil
	}
	return sql.Open("sqlite", defaultPath)
}

// DialectSchema translates SQLite table creation schemas to Postgres if needed.
func DialectSchema(schema string) string {
	if ActiveDriver() != "postgres" {
		return schema
	}
	s := schema
	s = strings.ReplaceAll(s, "INTEGER PRIMARY KEY AUTOINCREMENT", "SERIAL PRIMARY KEY")
	s = strings.ReplaceAll(s, "REAL", "DOUBLE PRECISION")
	s = strings.ReplaceAll(s, "PRAGMA journal_mode=WAL;", "")
	// Postgres doesn't support 'OR IGNORE', convert to ON CONFLICT DO NOTHING
	s = strings.ReplaceAll(s, "INSERT OR IGNORE", "INSERT")
	return s
}

var qMarkRegex = regexp.MustCompile(`\?`)

// PrepareQuery translates placeholders from '?' to '$1', '$2', etc. if active driver is postgres.
func PrepareQuery(query string) string {
	if ActiveDriver() != "postgres" {
		return query
	}
	n := 1
	return qMarkRegex.ReplaceAllStringFunc(query, func(string) string {
		placeholder := fmt.Sprintf("$%d", n)
		n++
		return placeholder
	})
}

// InsertAndGetID executes an INSERT statement and returns the auto-incremented ID.
func InsertAndGetID(db *sql.DB, query string, args ...any) (int64, error) {
	if ActiveDriver() == "postgres" {
		q := query
		if !strings.Contains(strings.ToUpper(q), "RETURNING") {
			q = strings.TrimSpace(q)
			q = q + " RETURNING id"
		}
		q = PrepareQuery(q)
		var id int64
		err := db.QueryRow(q, args...).Scan(&id)
		return id, err
	}

	res, err := db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TxInsertAndGetID is the transaction version of InsertAndGetID.
func TxInsertAndGetID(tx *sql.Tx, query string, args ...any) (int64, error) {
	if ActiveDriver() == "postgres" {
		q := query
		if !strings.Contains(strings.ToUpper(q), "RETURNING") {
			q = strings.TrimSpace(q)
			q = q + " RETURNING id"
		}
		q = PrepareQuery(q)
		var id int64
		err := tx.QueryRow(q, args...).Scan(&id)
		return id, err
	}

	res, err := tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Exec executes a query after converting placeholders if needed.
func Exec(db *sql.DB, query string, args ...any) (sql.Result, error) {
	return db.Exec(PrepareQuery(query), args...)
}

// Query executes a query after converting placeholders if needed.
func Query(db *sql.DB, query string, args ...any) (*sql.Rows, error) {
	return db.Query(PrepareQuery(query), args...)
}

// QueryRow executes a query after converting placeholders if needed.
func QueryRow(db *sql.DB, query string, args ...any) *sql.Row {
	return db.QueryRow(PrepareQuery(query), args...)
}

// TxExec is the transaction version of Exec.
func TxExec(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.Exec(PrepareQuery(query), args...)
}

// TxQuery is the transaction version of Query.
func TxQuery(tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
	return tx.Query(PrepareQuery(query), args...)
}

// TxQueryRow is the transaction version of QueryRow.
func TxQueryRow(tx *sql.Tx, query string, args ...any) *sql.Row {
	return tx.QueryRow(PrepareQuery(query), args...)
}
