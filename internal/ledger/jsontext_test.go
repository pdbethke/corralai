// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

func TestJSONTextRoundTrips(t *testing.T) {
	in := []ledger.ToolMutantCounts{{Path: "a.ts", Tool: "stryker@10.0.0"}}
	enc, err := ledger.EncodeJSONText(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := enc.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: got %+v want %+v", out, in)
	}
}

func TestJSONTextEmptyIsEmptyText(t *testing.T) {
	enc, err := ledger.EncodeJSONText([]ledger.ToolMutantCounts(nil))
	if err != nil || enc != "" {
		t.Fatalf("nil slice: got %q, %v; want empty text", enc, err)
	}
	enc, err = ledger.EncodeJSONText([]ledger.ToolMutantCounts{})
	if err != nil || enc != "" {
		t.Fatalf("empty slice: got %q, %v; want empty text", enc, err)
	}
	m, err := ledger.EncodeJSONText(map[string]int(nil))
	if err != nil || m != "" {
		t.Fatalf("nil map: got %q, %v", m, err)
	}
	var zero ledger.JSONText[[]ledger.ToolMutantCounts]
	got, err := zero.Decode()
	if err != nil || got != nil {
		t.Fatalf("decode empty: got %v, %v; want nil, nil", got, err)
	}
}

func TestJSONTextEncodedType(t *testing.T) {
	var j ledger.JSONText[[]ledger.ToolMutantCounts]
	if got, want := j.EncodedType(), reflect.TypeOf([]ledger.ToolMutantCounts(nil)); got != want {
		t.Fatalf("EncodedType = %v, want %v", got, want)
	}
}

func TestScanRowMarshalsJSONTextAsString(t *testing.T) {
	x, err := ledger.EncodeJSONText([]ledger.ToolMutantCounts{{Path: "a.ts", Tool: "stryker@10.0.0"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ledger.ScanRow{ToolMutants: x})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if s, ok := m["ToolMutants"].(string); !ok || s != `[{"path":"a.ts","tool":"stryker@10.0.0"}]` {
		t.Fatalf("ToolMutants not a JSON string with the old bytes: %s", b)
	}
	// Golden: the whole row is byte-identical to the old string field.
	old := struct{ ToolMutants string }{`[{"path":"a.ts","tool":"stryker@10.0.0"}]`}
	ob, _ := json.Marshal(old)
	if want := string(ob); !strings.Contains(string(b), want[1:len(want)-1]) {
		t.Fatalf("bytes differ from the old string field: %s vs %s", b, ob)
	}
}
