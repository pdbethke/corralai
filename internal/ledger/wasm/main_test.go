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
	"time"

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
	// The page draws Summary and computes nothing, so it must cross the
	// boundary whole; read it from the raw JSON, not through the Go type.
	var raw struct {
		Summary *struct {
			Totals *struct {
				Scans *int `json:"scans"`
			} `json:"totals"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Summary == nil || raw.Summary.Totals == nil || raw.Summary.Totals.Scans == nil || *raw.Summary.Totals.Scans != 2 {
		t.Fatalf("want summary.totals.scans == 2 in %s", out)
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

// The keyless path runs in the browser too: the embedded Sigstore root
// parsed, the repository is required, and a keyless entry reaches the bundle
// check. (A genuinely signed keyless entry needs a public-good signature,
// which only a real Actions run can make; internal/ledger's differential and
// real-bundle tests cover the verifier itself.)
func TestDeriveKeylessReachesTheVerifier(t *testing.T) {
	e := ledger.Entry{Format: ledger.Format6, KeyID: ledger.KeyIDSigstore, Pushed: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
		Bundle: ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "c", ScanID: 1}}}
	e.ScanUID = ledger.ScanUID(e.Bundle.Scan, e.Pushed)
	e.Bundle.Scan.ScanUID = e.ScanUID
	h, err := ledger.EntryHash(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Hash = h
	e.Sigstore = json.RawMessage(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.1+json"}`)
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for repo, want := range map[string]string{"": ledger.ProblemNoLedgerRepo, "acme/widgets": "bundle media type"} {
		arr := js.Global().Get("Array").New()
		u8 := js.Global().Get("Uint8Array").New(len(b))
		js.CopyBytesToJS(u8, b)
		o := js.Global().Get("Object").New()
		o.Set("name", "k.json")
		o.Set("bytes", u8)
		arr.Call("push", o)
		tr := trust("")
		tr.Set("repo", repo)
		out, thrown := call(arr, tr)
		if thrown != "" {
			t.Fatalf("threw: %s", thrown)
		}
		var d ledger.Derived
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Entries) != 1 || d.Entries[0].Status != ledger.StatusFailed || !strings.Contains(d.Entries[0].Problem, want) {
			t.Fatalf("repo %q: want a failed entry naming %q, got %+v", repo, want, d.Entries)
		}
	}
}

// rawJS is one in-memory entry as the page hands it over.
func rawJS(arr js.Value, name string, e ledger.Entry, t *testing.T) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	u8 := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u8, b)
	o := js.Global().Get("Object").New()
	o.Set("name", name)
	o.Set("bytes", u8)
	arr.Call("push", o)
}

// Who signed and what an entry means for the chain (a retraction's reason)
// are measured in Go and must survive the boundary into the page: a
// field-by-field status that leaves them out drops them silently.
func TestDeriveCarriesSignerAndNoteToJS(t *testing.T) {
	pub, err := os.ReadFile(filepath.Join(fixtureDir, "rootpub.hex"))
	if err != nil {
		t.Fatal(err)
	}
	out, thrown := call(jsEntries(t), trust(strings.TrimSpace(string(pub))))
	if thrown != "" {
		t.Fatalf("threw: %s", thrown)
	}
	if n := strings.Count(out, `"signer":"root"`); n != 2 {
		t.Fatalf("want both fixture entries signed by root in the JS result, got %d in %s", n, out)
	}

	scan := ledger.Entry{Format: ledger.FileFormat, Pushed: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
		Bundle: ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "c", ScanID: 1}}}
	scan.ScanUID = ledger.ScanUID(scan.Bundle.Scan, scan.Pushed)
	scan.Bundle.Scan.ScanUID = scan.ScanUID
	if scan.Hash, err = ledger.EntryHash(scan); err != nil {
		t.Fatal(err)
	}
	rt := ledger.Entry{Format: ledger.FileFormat, Kind: ledger.KindRetract, Prev: scan.Hash, Retracts: scan.Hash,
		Reason: "wrong scope", Pushed: scan.Pushed.Add(time.Second)}
	if rt.Hash, err = ledger.EntryHash(rt); err != nil {
		t.Fatal(err)
	}
	arr := js.Global().Get("Array").New()
	rawJS(arr, "a.json", scan, t)
	rawJS(arr, "b.json", rt, t)
	out, thrown = call(arr, trust(""))
	if thrown != "" {
		t.Fatalf("threw: %s", thrown)
	}
	var d ledger.Derived
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 2 || !strings.Contains(d.Entries[1].Note, "wrong scope") || !strings.Contains(out, `"note":"retracts `) {
		t.Fatalf("the retraction's note did not reach the page: %s", out)
	}
}
