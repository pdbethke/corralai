// SPDX-License-Identifier: Elastic-2.0

//go:build js && wasm

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall/js"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

const fixtureDir = "../testdata/two-entries"

func jsEntries(t *testing.T) js.Value {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixtureDir, "scans", "*.json.gz"))
	if err != nil || len(files) != 2 {
		t.Fatalf("fixture: want 2 entries, got %v (%v)", files, err)
	}
	arr := js.Global().Get("Array").New()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		u8 := js.Global().Get("Uint8Array").New(len(b))
		js.CopyBytesToJS(u8, b)
		o := js.Global().Get("Object").New()
		o.Set("name", filepath.Base(f))
		o.Set("bytes", u8)
		arr.Call("push", o)
	}
	return arr
}

func trust(pub string) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("rootPub", pub)
	o.Set("repo", "")
	return o
}

// call invokes the registered global; a JS throw surfaces as js.Error.
func call(entries, tr js.Value) (res string, thrown string) {
	register()
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(js.Error); ok {
				thrown = e.Value.Get("message").String()
				return
			}
			panic(r)
		}
	}()
	return js.Global().Get("corralLedgerDerive").Invoke(entries, tr).String(), ""
}

func TestDeriveFromJS(t *testing.T) {
	pub, err := os.ReadFile(filepath.Join(fixtureDir, "rootpub.hex"))
	if err != nil {
		t.Fatal(err)
	}
	out, thrown := call(jsEntries(t), trust(strings.TrimSpace(string(pub))))
	if thrown != "" {
		t.Fatalf("threw: %s", thrown)
	}
	var d ledger.Derived
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	if d.Problem != "" || len(d.Scans) != 2 {
		t.Fatalf("problem=%q scans=%d", d.Problem, len(d.Scans))
	}
	if n := strings.Count(out, `"status":"verified"`); n != 2 {
		t.Fatalf("verified count %d in %s", n, out)
	}
}

func TestDeriveRegisteredGlobal(t *testing.T) {
	register()
	if js.Global().Get("corralLedgerDerive").Type() != js.TypeFunction {
		t.Fatal("corralLedgerDerive not registered")
	}
}

func TestDeriveRefusesBadInput(t *testing.T) {
	good := jsEntries(t)
	noBytes := js.Global().Get("Array").New()
	o := js.Global().Get("Object").New()
	o.Set("name", "x.json")
	noBytes.Call("push", o)
	cases := []struct {
		name    string
		entries js.Value
		trust   js.Value
		want    string
	}{
		{"bad hex", good, trust("zz"), "rootPub"},
		{"short key", good, trust("abcd"), "rootPub"},
		{"missing bytes", noBytes, trust(""), "bytes"},
		{"entries not array", js.ValueOf("x"), trust(""), "entries"},
	}
	for _, c := range cases {
		_, thrown := call(c.entries, c.trust)
		if !strings.Contains(thrown, c.want) {
			t.Errorf("%s: thrown %q, want it to name %q", c.name, thrown, c.want)
		}
	}
}
