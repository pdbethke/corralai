// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// Every reader gets its level from the walk, not from its own reading of the
// flags: an ed25519 entry is "key" only when its signature was checked, a
// keyless one is unpinned until Task 3's pin, and an entry with a Problem or
// an unchecked signature has no level at all.
func TestLevelPerEntry(t *testing.T) {
	s := newTestSigstore(t)
	rootKey := key(0x51)
	pub := rootKey.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	scan(t, dir, 1, signerFor(rootKey))
	signed := readBytes(t, dir)
	k := s.keylessEntry(t, signed[0].Hash, 2, spec{})
	bad := s.keylessEntry(t, "", 3, spec{})

	unsignedDir := t.TempDir()
	scan(t, unsignedDir, 1, nil)
	unsigned := readBytes(t, unsignedDir)

	cases := []struct {
		name    string
		entries []ledger.Entry
		trust   ledger.Trust
		want    ledger.SigLevel
		problem bool
	}{
		{"unsigned", unsigned, ledger.Trust{}, ledger.LevelUnsigned, false},
		{"ed25519 read with --pub", signed, ledger.Trust{RootPub: pub}, ledger.LevelKey, false},
		{"ed25519 read without --pub: not checked", signed, ledger.Trust{}, "", false},
		{"keyless", append(append([]ledger.Entry{}, signed...), k), ledger.Trust{RootPub: pub, Repo: testRepo, SigstoreRoot: s.ledgerRoot(t)}, ledger.LevelKeylessUnpinned, false},
		{"keyless whose bundle does not verify for the named repository", []ledger.Entry{bad}, ledger.Trust{Repo: "acme/other", SigstoreRoot: s.ledgerRoot(t)}, "", true},
	}
	for _, c := range cases {
		checks := ledger.Verify(c.entries, c.trust)
		got := checks[len(checks)-1]
		if got.Level != c.want || (got.Problem != "") != c.problem {
			t.Errorf("%s: level %q problem %q, want level %q (problem %v)", c.name, got.Level, got.Problem, c.want, c.problem)
		}
	}
}

// An unsigned entry among signed ones is still unsigned, not unranked.
func TestLevelUnsignedAmongSigned(t *testing.T) {
	s := newTestSigstore(t)
	dir := t.TempDir()
	scan(t, dir, 1, nil)
	es := readBytes(t, dir)
	es = append(es, s.keylessEntry(t, es[0].Hash, 2, spec{}))
	checks := ledger.Verify(es, ledger.Trust{Repo: testRepo, SigstoreRoot: s.ledgerRoot(t)})
	if checks[0].Level != ledger.LevelUnsigned || checks[1].Level != ledger.LevelKeylessUnpinned {
		t.Fatalf("levels %q, %q (%+v)", checks[0].Level, checks[1].Level, checks)
	}
}

func TestEverySigLevelHasASentence(t *testing.T) {
	all := []ledger.SigLevel{ledger.LevelUnsigned, ledger.LevelKeylessUnpinned, ledger.LevelKeylessPinned, ledger.LevelKeylessPinnedUnchecked, ledger.LevelKey}
	seen := map[string]ledger.SigLevel{}
	for _, l := range all {
		s := ledger.LevelSentence(l)
		if s == "" {
			t.Errorf("level %q has no sentence", l)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("levels %q and %q share a sentence", prev, l)
		}
		seen[s] = l
	}
	if ledger.LevelSentence("") != "" {
		t.Error("the empty level must have no sentence")
	}
}

// Derive carries the level and its words so a page never re-derives them.
func TestDeriveCarriesTheLevel(t *testing.T) {
	rootKey := key(0x52)
	pub := rootKey.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	scan(t, dir, 1, signerFor(rootKey))
	var raw []ledger.RawEntry
	for _, e := range readBytes(t, dir) {
		raw = append(raw, ledger.RawEntry{Name: e.File, Bytes: e.Raw})
	}
	d, err := ledger.Derive(raw, ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 1 || d.Entries[0].Level != ledger.LevelKey || d.Entries[0].LevelSentence != ledger.LevelSentence(ledger.LevelKey) {
		t.Fatalf("entry status = %+v", d.Entries)
	}
}
