// SPDX-License-Identifier: Elastic-2.0

//go:build js && wasm

// Command wasm exposes ledger.Derive to JavaScript as the global
// corralLedgerDerive(entries, trust). It is the browser's whole copy of the
// ledger's rules: this file only converts values across the JS boundary and
// refuses malformed input; every rule lives in ledger.Derive, so the CLI and
// the page cannot disagree.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"syscall/js"

	"github.com/pdbethke/corralai/internal/ledger"
)

// thrower turns the {error|value} record the Go side returns into a JS throw:
// a Go js.Func cannot throw, and a panic would kill the whole runtime.
const thrower = `(inner) => (entries, trust) => {
  const r = inner(entries, trust);
  if (r.error !== undefined) throw new Error(r.error);
  return r.value;
}`

func register() {
	inner := js.FuncOf(deriveJS)
	wrapped := js.Global().Get("Function").New("return " + thrower).Invoke().Invoke(inner)
	js.Global().Set("corralLedgerDerive", wrapped)
}

func main() {
	register()
	select {}
}

func deriveJS(_ js.Value, args []js.Value) any {
	out, err := derive(args)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"value": out}
}

func derive(args []js.Value) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("corralLedgerDerive(entries, trust): want 2 arguments, got %d", len(args))
	}
	entries, trust := args[0], args[1]
	if !js.Global().Get("Array").Call("isArray", entries).Bool() {
		return "", fmt.Errorf("entries must be an array of {name, bytes}")
	}
	if trust.Type() != js.TypeObject {
		return "", fmt.Errorf("trust must be an object {rootPub, repo}")
	}
	var t ledger.Trust
	for _, f := range []string{"rootPub", "repo"} {
		if trust.Get(f).Type() != js.TypeString {
			return "", fmt.Errorf("trust.%s must be a string (\"\" when unset)", f)
		}
	}
	t.Repo = trust.Get("repo").String()
	if h := trust.Get("rootPub").String(); h != "" {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return "", fmt.Errorf("trust.rootPub must be %d bytes of hex", ed25519.PublicKeySize)
		}
		t.RootPub = ed25519.PublicKey(b)
	}
	u8 := js.Global().Get("Uint8Array")
	raw := make([]ledger.RawEntry, entries.Length())
	for i := range raw {
		e := entries.Index(i)
		if e.Type() != js.TypeObject || e.Get("name").Type() != js.TypeString {
			return "", fmt.Errorf("entries[%d].name must be a string", i)
		}
		bv := e.Get("bytes")
		if bv.Type() != js.TypeObject || !bv.InstanceOf(u8) {
			return "", fmt.Errorf("entries[%d].bytes must be a Uint8Array", i)
		}
		b := make([]byte, bv.Get("length").Int())
		js.CopyBytesToGo(b, bv)
		raw[i] = ledger.RawEntry{Name: e.Get("name").String(), Bytes: b}
	}
	d, err := ledger.Derive(raw, t)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
