// SPDX-License-Identifier: Elastic-2.0

package controlgate

import "testing"

func TestParseControlPolicies(t *testing.T) {
	pols, bad := ParseControlPolicies("repo=o/r,owner=ciso@bankz,lang=go,base=main; repo=o/r2,owner=lead@x,lang=go")
	if len(pols) != 2 || len(bad) != 0 {
		t.Fatalf("pols=%+v bad=%+v", pols, bad)
	}
	if pols[0].Repo != "o/r" || pols[0].Owner != "ciso@bankz" || pols[0].Lang != "go" || pols[0].Base != "main" {
		t.Fatalf("pol0 wrong: %+v", pols[0])
	}
	// missing owner; missing repo; unknown lang → all three are bad.
	_, bad2 := ParseControlPolicies("repo=o/r,lang=go; owner=x,lang=go; repo=o/r,owner=x,lang=rust")
	if len(bad2) != 3 {
		t.Fatalf("expected 3 bad, got %+v", bad2)
	}
	// omitted lang defaults to go (valid).
	pols3, bad3 := ParseControlPolicies("repo=o/r,owner=x")
	if len(pols3) != 1 || len(bad3) != 0 || pols3[0].Lang != "go" {
		t.Fatalf("default lang: pols=%+v bad=%+v", pols3, bad3)
	}
	if p, _ := ParseControlPolicies(""); p != nil {
		t.Fatal("empty raw → nil policies (off switch)")
	}
}

func TestLangScaffold(t *testing.T) {
	base, cmd, ok := LangScaffold("go")
	if !ok || base["go.mod"] == "" || len(cmd) == 0 {
		t.Fatalf("go scaffold: base=%v cmd=%v ok=%v", base, cmd, ok)
	}
	if _, _, ok := LangScaffold("rust"); ok {
		t.Fatal("unknown lang must be !ok")
	}
	if _, _, ok := LangScaffold(""); ok {
		t.Fatal("empty lang must be !ok")
	}
}
