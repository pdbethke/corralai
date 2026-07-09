// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

func TestResolveHerdInputs(t *testing.T) {
	dir := t.TempDir()
	gw, err := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Close() })
	ta, err := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ta.Close() })

	if err := gw.Register(gateway.Endpoint{Name: "prod-db", Transport: "stdio", Endpoint: "x", Enabled: true}, gateway.Auth{}, ""); err != nil {
		t.Fatal(err)
	}
	lbID, err := ta.SaveLookbookItem("neon", "Emulate the neon mock.", "image/png", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("happy path", func(t *testing.T) {
		endpointNames, guidelines, err := ResolveHerdInputs(gw, ta, "", []string{"prod-db"}, []int64{lbID})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(endpointNames) != 1 || endpointNames[0] != "prod-db" {
			t.Fatalf("endpointNames = %v", endpointNames)
		}
		if len(guidelines) != 1 || guidelines[0] != "neon: Emulate the neon mock." {
			t.Fatalf("guidelines = %v", guidelines)
		}
	})

	t.Run("unknown endpoint", func(t *testing.T) {
		_, _, err := ResolveHerdInputs(gw, ta, "", []string{"nope"}, nil)
		if err == nil {
			t.Fatal("expected error for unknown endpoint")
		}
	})

	t.Run("unknown lookbook item", func(t *testing.T) {
		_, _, err := ResolveHerdInputs(gw, ta, "", nil, []int64{999999})
		if err == nil {
			t.Fatal("expected error for unknown lookbook item")
		}
	})

	t.Run("nil stores skip validation", func(t *testing.T) {
		endpointNames, guidelines, err := ResolveHerdInputs(nil, nil, "", []string{"anything"}, []int64{1})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if endpointNames != nil || guidelines != nil {
			t.Fatalf("expected nil results, got %v %v", endpointNames, guidelines)
		}
	})
}
