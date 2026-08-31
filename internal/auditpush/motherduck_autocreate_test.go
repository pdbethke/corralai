// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsMissingMotherDuckDatabase pins the exact classifier: only the
// specific "no database/share named" Catalog Error MotherDuck raises on a
// first ATTACH to a database nobody has created yet counts — every other
// ATTACH failure (a bad token, a network error, a genuinely misspelled
// share whose OWN message differs) must not.
func TestIsMissingMotherDuckDatabase(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"the literal MotherDuck message", errors.New(`Catalog Error: no database/share named 'corral_live' found`), true},
		{"wrapped", fmt.Errorf("auditpush: attach %q: %w", "md:corral_live", errors.New(`Catalog Error: no database/share named 'corral_live' found`)), true},
		{"a different catalog error", errors.New(`Catalog Error: table "foo" does not exist`), false},
		{"a bad token", errors.New(`IO Error: Failed to download extension`), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingMotherDuckDatabase(tc.err); got != tc.want {
				t.Errorf("isMissingMotherDuckDatabase(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestMotherDuckDBName pins extraction (including a "?"-delimited DSN and a
// "/"-delimited share path) and, separately, the refusal: a name outside
// [A-Za-z0-9_]+ is a hard error, never quoted into DDL.
func TestMotherDuckDBName(t *testing.T) {
	cases := []struct {
		target  string
		want    string
		wantErr bool
	}{
		{"md:corral_live", "corral_live", false},
		{"md:corral_live?motherduck_token=x", "corral_live", false},
		{"md:corral_live/some/share/path", "corral_live", false},
		{"md:", "", true},
		{`md:foo"; DROP TABLE x; --`, "", true},
		{"md:has spaces", "", true},
		{"md:has-a-dash", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			got, err := motherDuckDBName(tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("motherDuckDBName(%q) = %q, <nil>, want an error", tc.target, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("motherDuckDBName(%q): %v", tc.target, err)
			}
			if got != tc.want {
				t.Errorf("motherDuckDBName(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}

// TestAttachWithAutoCreateSucceedsWithoutTouchingCreate is the ordinary
// path: attach succeeds on the first try, so createDB must never be
// called.
func TestAttachWithAutoCreateSucceedsWithoutTouchingCreate(t *testing.T) {
	attachCalls, createCalls := 0, 0
	err := attachWithAutoCreate("md:corral_live",
		func() error { attachCalls++; return nil },
		func(string) error { createCalls++; return nil })
	if err != nil {
		t.Fatalf("attachWithAutoCreate: %v", err)
	}
	if attachCalls != 1 || createCalls != 0 {
		t.Errorf("attachCalls=%d createCalls=%d, want 1,0", attachCalls, createCalls)
	}
}

// TestAttachWithAutoCreateCreatesThenRetriesOnce is the headline case: a
// first ATTACH failing with the missing-database Catalog Error triggers
// exactly one CREATE DATABASE, then exactly one retry of attach.
func TestAttachWithAutoCreateCreatesThenRetriesOnce(t *testing.T) {
	missing := errors.New(`Catalog Error: no database/share named 'corral_live' found`)
	attachCalls, createCalls := 0, 0
	err := attachWithAutoCreate("md:corral_live",
		func() error {
			attachCalls++
			if attachCalls == 1 {
				return missing
			}
			return nil
		},
		func(name string) error {
			createCalls++
			if name != "corral_live" {
				t.Errorf("createDB got name %q, want corral_live", name)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("attachWithAutoCreate: %v", err)
	}
	if attachCalls != 2 {
		t.Errorf("attachCalls = %d, want 2 (the original failure plus exactly one retry)", attachCalls)
	}
	if createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", createCalls)
	}
}

// TestAttachWithAutoCreatePropagatesOtherErrorsUnchanged: an ATTACH failure
// that is NOT the missing-database Catalog Error must never trigger a
// create or a retry — a bad token or a network error is not "this database
// doesn't exist yet".
func TestAttachWithAutoCreatePropagatesOtherErrorsUnchanged(t *testing.T) {
	other := errors.New("IO Error: could not reach motherduck.com")
	attachCalls, createCalls := 0, 0
	err := attachWithAutoCreate("md:corral_live",
		func() error { attachCalls++; return other },
		func(string) error { createCalls++; return nil })
	if !errors.Is(err, other) {
		t.Fatalf("attachWithAutoCreate = %v, want it to wrap %v unchanged", err, other)
	}
	if attachCalls != 1 || createCalls != 0 {
		t.Errorf("attachCalls=%d createCalls=%d, want 1,0 — no retry, no create, for an unrelated error", attachCalls, createCalls)
	}
}

// TestAttachWithAutoCreateNeverTriesOnALocalPath: the missing-database
// recovery is MotherDuck-specific — a local DuckDB path that fails to
// attach for some OTHER reason (permissions, a corrupt file) must never
// have CREATE DATABASE attempted against it, and the error class check
// (isMissingMotherDuckDatabase) is irrelevant since the "md:" prefix gate
// comes first.
func TestAttachWithAutoCreateNeverTriesOnALocalPath(t *testing.T) {
	local := errors.New(`Catalog Error: no database/share named 'x' found`) // contrived, but even if a local path produced this text
	attachCalls, createCalls := 0, 0
	err := attachWithAutoCreate("/tmp/warehouse.duckdb",
		func() error { attachCalls++; return local },
		func(string) error { createCalls++; return nil })
	if !errors.Is(err, local) {
		t.Fatalf("attachWithAutoCreate = %v, want it to wrap %v unchanged", err, local)
	}
	if createCalls != 0 {
		t.Errorf("createCalls = %d, want 0 — a local path must never attempt CREATE DATABASE", createCalls)
	}
}

// TestAttachWithAutoCreateSurfacesACreateFailureVerbatim: CREATE DATABASE
// against a SHARE fails (a share is read-only, not a database an account
// can create into) — that failure must surface, not be swallowed or
// retried a second time.
func TestAttachWithAutoCreateSurfacesACreateFailureVerbatim(t *testing.T) {
	missing := errors.New(`Catalog Error: no database/share named 'a_share' found`)
	createFail := errors.New(`Catalog Error: cannot create a database with the name of an existing share`)
	attachCalls, createCalls := 0, 0
	err := attachWithAutoCreate("md:a_share",
		func() error { attachCalls++; return missing },
		func(string) error { createCalls++; return createFail })
	if err == nil {
		t.Fatal("expected the create failure to surface")
	}
	if attachCalls != 1 {
		t.Errorf("attachCalls = %d, want 1 — a failed create must not retry attach", attachCalls)
	}
	if createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", createCalls)
	}
}
