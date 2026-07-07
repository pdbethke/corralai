// SPDX-License-Identifier: Elastic-2.0

package dbutil

import (
	"os"
	"testing"
)

func TestPrepareQuery(t *testing.T) {
	_ = os.Setenv("CORRALAI_POSTGRES_URL", "postgres://localhost/test")
	defer os.Unsetenv("CORRALAI_POSTGRES_URL")

	q := "INSERT INTO users (name, email, age) VALUES (?, ?, ?)"
	expected := "INSERT INTO users (name, email, age) VALUES ($1, $2, $3)"
	actual := PrepareQuery(q)

	if actual != expected {
		t.Errorf("expected %q, got %q", expected, actual)
	}
}

func TestDialectSchema(t *testing.T) {
	_ = os.Setenv("CORRALAI_POSTGRES_URL", "postgres://localhost/test")
	defer os.Unsetenv("CORRALAI_POSTGRES_URL")

	schema := "CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, rating REAL)"
	expected := "CREATE TABLE users (id SERIAL PRIMARY KEY, rating DOUBLE PRECISION)"
	actual := DialectSchema(schema)

	if actual != expected {
		t.Errorf("expected %q, got %q", expected, actual)
	}
}
