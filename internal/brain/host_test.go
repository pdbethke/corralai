// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/rolemodel"
)

func TestTopologyShowsExpectedAndDrift(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })

	// Quill: role=reviewer, model=ollama-x
	book := NewHostBook()
	book.Set(Host{Agent: "Quill", Role: "reviewer", Model: "ollama-x", Backend: "ollama", TS: 9_999_999_999})

	// Policy: reviewer=claude-opus → drift expected
	p := rolemodel.New()
	p.Set("reviewer", rolemodel.ModelRef{Model: "claude-opus"})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{HostBook: book, RoleModels: p}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "obs", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var out topologyOut
	callTask(t, sess, "swarm_topology", map[string]any{}, &out)
	if len(out.Hosts) != 1 {
		t.Fatalf("want 1 host, got %d", len(out.Hosts))
	}
	h := out.Hosts[0]
	if h.Expected != "claude-opus" {
		t.Errorf("Expected: got %q, want claude-opus", h.Expected)
	}
	if !h.Drift {
		t.Error("Drift: want true (ollama-x != claude-opus), got false")
	}
	if out.Policy == nil || out.Policy["reviewer"].Model != "claude-opus" {
		t.Errorf("Policy not in response or wrong: %v", out.Policy)
	}
}

func TestAnnotatedHostMirrorsHost(t *testing.T) {
	// AnnotatedHost is a flat copy of Host plus 2 annotation fields (Expected,
	// Drift). If a field is added to Host but not mirrored into AnnotatedHost,
	// it silently drops from every swarm_topology response — this guard fails
	// loudly the moment the two structs diverge.
	const annotationFields = 2
	hostFields := reflect.TypeOf(Host{}).NumField()
	annotatedFields := reflect.TypeOf(AnnotatedHost{}).NumField()
	if annotatedFields != hostFields+annotationFields {
		t.Fatalf("AnnotatedHost has %d fields, want Host's %d + %d annotation fields = %d; keep AnnotatedHost in sync with Host (see the SYNC comment in host.go)",
			annotatedFields, hostFields, annotationFields, hostFields+annotationFields)
	}
}

func TestAnnotateHostsNilPolicyDegrades(t *testing.T) {
	hosts := []Host{
		{Agent: "Quill", Role: "reviewer", Model: "ollama-x", Backend: "ollama", TS: 1},
		{Agent: "Sable", Role: "builder", Model: "claude-opus", Backend: "anthropic", TS: 1},
	}
	// nil Policy must not panic and must produce no expected/no drift.
	got := AnnotateHosts(hosts, nil)
	if len(got) != len(hosts) {
		t.Fatalf("got %d annotated, want %d", len(got), len(hosts))
	}
	for _, h := range got {
		if h.Expected != "" {
			t.Errorf("%s: Expected=%q, want empty under nil policy", h.Agent, h.Expected)
		}
		if h.Drift {
			t.Errorf("%s: Drift=true, want false under nil policy", h.Agent)
		}
	}
}

func TestAvailableModelsRespectsPresenceWindow(t *testing.T) {
	now := int64(10000)
	window := int64(300)

	book := NewHostBook()
	// Agent A: within window
	book.Set(Host{Agent: "agentA", Backend: "anthropic", Model: "claude-opus", TS: now})
	// Agent B: stale (now-10000 is far outside the 300s window)
	book.Set(Host{Agent: "agentB", Backend: "ollama", Model: "qwen", TS: now - 10000})

	refs := book.AvailableModels(window, now)
	if len(refs) != 1 {
		t.Fatalf("expected 1 available model, got %d: %v", len(refs), refs)
	}
	if refs[0].Model != "claude-opus" || refs[0].Backend != "anthropic" {
		t.Errorf("expected anthropic/claude-opus, got %+v", refs[0])
	}

	t.Run("deduplicates same model from two agents", func(t *testing.T) {
		book2 := NewHostBook()
		book2.Set(Host{Agent: "agentC", Backend: "anthropic", Model: "claude-opus", TS: now})
		book2.Set(Host{Agent: "agentD", Backend: "anthropic", Model: "claude-opus", TS: now - 10})
		refs2 := book2.AvailableModels(window, now)
		if len(refs2) != 1 {
			t.Errorf("expected 1 deduplicated entry, got %d: %v", len(refs2), refs2)
		}
		want := rolemodel.ModelRef{Backend: "anthropic", Model: "claude-opus"}
		if refs2[0] != want {
			t.Errorf("expected %+v, got %+v", want, refs2[0])
		}
	})
}
