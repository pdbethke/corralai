// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// memBackend is a writable in-memory backend for exercising Store.
type memBackend struct{ m map[string]string }

func newMem() *memBackend                                { return &memBackend{m: map[string]string{}} }
func (b *memBackend) get(n string) (string, bool, error) { v, ok := b.m[n]; return v, ok, nil }
func (b *memBackend) set(n, v string) error              { b.m[n] = v; return nil }
func (b *memBackend) remove(n string) error              { delete(b.m, n); return nil }
func (b *memBackend) names() ([]string, error) {
	var out []string
	for k := range b.m {
		out = append(out, k)
	}
	return out, nil
}
func (b *memBackend) writable() bool { return true }

func TestStoreGetFirstHitWins(t *testing.T) {
	hi, lo := newMem(), newMem()
	hi.set("K", "from-hi")
	lo.set("K", "from-lo")
	s := newStore(hi, lo)
	v, ok, err := s.Get("K")
	if err != nil || !ok || v != "from-hi" {
		t.Fatalf("Get = %q ok=%v err=%v, want from-hi", v, ok, err)
	}
	if _, ok, _ := s.Get("MISSING"); ok {
		t.Fatal("missing key must report not-found")
	}
}

func TestStoreSetGoesToFirstWritable(t *testing.T) {
	ro, w := envBackend{}, newMem()
	s := newStore(ro, w) // env (read-only) first, mem writable second
	if err := s.Set("K", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v := w.m["K"]; v != "v" {
		t.Fatalf("Set landed in %q, want the writable backend", v)
	}
}

func TestStoreListUnionSortedDeduped(t *testing.T) {
	a, b := newMem(), newMem()
	a.set("B", "1")
	a.set("A", "2")
	b.set("A", "3")
	b.set("C", "4")
	s := newStore(a, b)
	got, _ := s.List()
	want := []string{"A", "B", "C"}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List = %v, want %v (deduped+sorted)", got, want)
	}
}

func TestRedactNeverLeaks(t *testing.T) {
	secret := "sk-supersecretvalue-1234567890"
	r := Redact(secret)
	if strings.Contains(r, "supersecret") || strings.Contains(r, secret) {
		t.Fatalf("Redact leaked the secret: %q", r)
	}
	if !strings.HasPrefix(r, "sk-s") { // first 4 shown for identification
		t.Fatalf("Redact should keep a 4-char fingerprint, got %q", r)
	}
	if Redact("") != "(empty)" {
		t.Fatalf("Redact(empty) = %q", Redact(""))
	}
}

func TestRedactShortSecretsNeverLeak(t *testing.T) {
	for _, s := range []string{"a", "ab", "abcd", "secret7", "eightchr"} { // len 1..8
		r := Redact(s)
		if strings.Contains(r, s) {
			t.Fatalf("Redact(%q) = %q leaked the value", s, r)
		}
	}
	// A long secret still keeps a 4-char prefix for identification.
	long := "sk-proj-abcdef1234567890"
	if r := Redact(long); !strings.HasPrefix(r, "sk-p") || strings.Contains(r, "1234567890") {
		t.Fatalf("Redact(long) = %q, want a 4-char prefix without the tail", r)
	}
}

func TestEnvBackend(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "envval")
	b := envBackend{}
	v, ok, _ := b.get("OPENAI_API_KEY")
	if !ok || v != "envval" {
		t.Fatalf("env get = %q ok=%v", v, ok)
	}
	if b.writable() {
		t.Fatal("env backend must be read-only")
	}
	if err := b.set("X", "y"); err == nil {
		t.Fatal("env set must return errReadOnly")
	}
	names, _ := b.names()
	found := false
	for _, n := range names {
		if n == "OPENAI_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("env names() should include the set canonical key, got %v", names)
	}
}

func TestOpenChainEnvOverridesStored(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("OPENROUTER_API_KEY", "stored"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Get("OPENROUTER_API_KEY"); !ok || v != "stored" {
		t.Fatalf("stored get = %q ok=%v", v, ok)
	}
	// env wins.
	t.Setenv("OPENROUTER_API_KEY", "fromenv")
	s2, _ := Open()
	if v, _, _ := s2.Get("OPENROUTER_API_KEY"); v != "fromenv" {
		t.Fatalf("env must override stored, got %q", v)
	}
}

// TestPresentProvidersResolvesTheWholeChain: the missing-model message used
// to probe os.Getenv alone, so a key saved with `corral secret set` read as
// "none". PresentProviders must see a key held by ANY backend, report each
// provider once per env name, and never carry a value.
func TestPresentProvidersResolvesTheWholeChain(t *testing.T) {
	for _, p := range Providers {
		t.Setenv(p.Env, "")
	}
	stored := newMem()
	stored.m["ANTHROPIC_API_KEY"] = "sk-ant-recognizable-fake"
	stored.m["OPENAI_API_KEY"] = "" // present-but-empty is absent
	s := newStore(envBackend{}, stored)

	got, err := s.PresentProviders()
	if err != nil {
		t.Fatal(err)
	}
	want := []Provider{{"ANTHROPIC_API_KEY", "Anthropic"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("PresentProviders = %v, want %v", got, want)
	}
	t.Setenv("GOOGLE_API_KEY", "g")
	got, _ = s.PresentProviders()
	if len(got) != 2 || got[1].Env != "GOOGLE_API_KEY" {
		t.Fatalf("env-held key not seen alongside the stored one: %v", got)
	}
}

// failingBackend errors for the names in bad and holds nothing otherwise.
type failingBackend struct {
	memBackend
	bad map[string]bool
}

func (b *failingBackend) get(n string) (string, bool, error) {
	if b.bad[n] {
		return "", false, errors.New("store unreadable")
	}
	return b.memBackend.get(n)
}

// TestPresentProvidersKeepsProbingAfterAStoreError: a real run resolves each
// name separately, so one unreadable name must not hide a key that a later
// provider holds in the environment. The error is still returned.
func TestPresentProvidersKeepsProbingAfterAStoreError(t *testing.T) {
	for _, p := range Providers {
		t.Setenv(p.Env, "")
	}
	last := Providers[len(Providers)-1]
	t.Setenv(last.Env, "x")
	bad := map[string]bool{}
	for _, p := range Providers[:len(Providers)-1] {
		bad[p.Env] = true
	}
	fb := &failingBackend{memBackend: *newMem(), bad: bad}
	s := newStore(envBackend{}, fb)
	got, err := s.PresentProviders()
	if err == nil {
		t.Fatal("store error was swallowed")
	}
	if len(got) != 1 || got[0] != last {
		t.Fatalf("PresentProviders = %v, want [%v] despite the store error", got, last)
	}
}

// TestCanonicalNamesCoverEveryProvider: CanonicalNames is derived from the
// provider table, so a provider added there is scrubbed and resolvable without
// a second edit — and nothing CanonicalNames held before was dropped.
func TestCanonicalNamesCoverEveryProvider(t *testing.T) {
	have := map[string]bool{}
	for _, n := range CanonicalNames {
		have[n] = true
	}
	for _, p := range Providers {
		if !have[p.Env] {
			t.Errorf("CanonicalNames lacks provider %s", p.Env)
		}
	}
	for _, n := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_API_KEY",
		"OPENROUTER_API_KEY", "CORRALAI_BRAIN_KEY", "CORRALAI_BRAIN_TOKEN", "GOOGLE_API_KEY"} {
		if !have[n] {
			t.Errorf("CanonicalNames lost %s", n)
		}
	}
}
