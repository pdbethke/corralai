// SPDX-License-Identifier: Elastic-2.0
package searchmerge

import "testing"

type row struct {
	id    string
	score float64
	via   string
}

func acc() Accessors[row] {
	return Accessors[row]{
		Key:      func(r row) string { return r.id },
		Score:    func(r *row) float64 { return r.score },
		SetScore: func(r *row, s float64) { r.score = s },
		SetVia:   func(r *row, v string) { r.via = v },
	}
}

func TestMergeNormalizesUnionsAndTags(t *testing.T) {
	kw := []row{{"a", 2, "keyword"}, {"b", 1, "keyword"}}
	sem := []row{{"a", 10, "semantic"}, {"c", 5, "semantic"}}
	out := Merge(kw, sem, acc(), 10)
	// a in both -> via "both", normalized scores in [0,1], sorted desc, 3 rows
	if len(out) != 3 {
		t.Fatalf("len=%d want 3", len(out))
	}
	if out[0].score > 1.0 {
		t.Fatalf("not normalized: %v", out[0].score)
	}
	var a *row
	for i := range out {
		if out[i].id == "a" {
			a = &out[i]
		}
	}
	if a == nil || a.via != "both" {
		t.Fatalf("a via=%v want both", a)
	}
	if kw[0].score != 2 {
		t.Fatalf("caller slice mutated: %v", kw[0].score)
	}
	if sem[0].score != 10 {
		t.Fatalf("caller slice mutated: %v", sem[0].score)
	}
}

func TestMergeSortDescAndTruncate(t *testing.T) {
	kw := []row{{"a", 1, "keyword"}, {"b", 2, "keyword"}, {"c", 3, "keyword"}}
	sem := []row{}
	out := Merge(kw, sem, acc(), 2)
	if len(out) != 2 {
		t.Fatalf("len=%d want 2 (truncated)", len(out))
	}
	if out[0].id != "c" || out[1].id != "b" {
		t.Fatalf("not sorted desc: %+v", out)
	}
}

func TestMergeDisjointKeysPreserved(t *testing.T) {
	kw := []row{{"a", 1, "keyword"}}
	sem := []row{{"b", 1, "semantic"}}
	out := Merge(kw, sem, acc(), 10)
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	for _, r := range out {
		if r.via == "both" {
			t.Fatalf("disjoint keys should not be tagged both: %+v", r)
		}
	}
}

func TestMergeCollisionKeepsMax(t *testing.T) {
	kw := []row{{"a", 1, "keyword"}}
	sem := []row{{"a", 10, "semantic"}}
	out := Merge(kw, sem, acc(), 10)
	if len(out) != 1 {
		t.Fatalf("len=%d want 1", len(out))
	}
	if out[0].score != 1.0 {
		t.Fatalf("expected normalized max score 1.0, got %v", out[0].score)
	}
	if out[0].via != "both" {
		t.Fatalf("via=%v want both", out[0].via)
	}
}
