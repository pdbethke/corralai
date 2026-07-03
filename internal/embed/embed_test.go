// SPDX-License-Identifier: Elastic-2.0

package embed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewForNilWhenNoURL(t *testing.T) {
	if NewFor("", "", "") != nil {
		t.Fatal("NewFor with empty url must return nil (graceful-degradation contract)")
	}
}

func TestEmbedRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		out := map[string]any{"data": []map[string]any{}}
		data := []map[string]any{}
		for range in.Input {
			data = append(data, map[string]any{"embedding": []float64{0.1, 0.2, 0.3}})
		}
		out["data"] = data
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	c := NewFor(srv.URL, "m", "")
	vecs, err := c.Embed([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 || vecs[0][0] != float32(0.1) {
		t.Fatalf("unexpected vecs: %v", vecs)
	}
}
