// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/review"
)

func rawDir(t *testing.T, dir string) []ledger.RawEntry {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(dir, auditpush.ScansSubdir))
	if err != nil {
		t.Fatal(err)
	}
	var out []ledger.RawEntry
	for _, d := range des {
		b, err := os.ReadFile(filepath.Join(dir, auditpush.ScansSubdir, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ledger.RawEntry{Name: d.Name(), Bytes: b})
	}
	return out
}

func pushScan(t *testing.T, dir string, id int64, audited int, s auditpush.LedgerSigner) {
	t.Helper()
	auditpush.SetLedgerSigner(s)
	t.Cleanup(func() { auditpush.SetLedgerSigner(nil) })
	b := ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "abcdef123456", ScanID: id, Audited: audited, CorralVersion: "vtest"}}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
}

// tamperFile flips a byte in one signed entry's bytes (written back as plain
// JSON, which ParseEntry accepts), without touching its stored hash.
func tamperFile(t *testing.T, dir, file, from, to string) {
	t.Helper()
	path := filepath.Join(dir, auditpush.ScansSubdir, file)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	if _, err := plain.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(plain.Bytes(), []byte(from), []byte(to), 1)
	if bytes.Equal(edited, plain.Bytes()) {
		t.Fatal("setup: nothing edited")
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
}

// mixedLedger: scans 1-3 signed, scan 2 retracted by a signed retraction, then
// one unsigned scan. One intact chain.
func mixedLedger(t *testing.T) (dir string, pub ed25519.PublicKey) {
	t.Helper()
	root := key(0x41)
	rs := signerFor(root)
	dir = t.TempDir()
	pushScan(t, dir, 1, 1, rs)
	pushScan(t, dir, 2, 1, rs)
	pushScan(t, dir, 3, 1, rs)
	es := readBytes(t, dir)
	if _, err := auditpush.WriteRetraction(dir, es[1].Hash, "wrong repo", rs); err != nil {
		t.Fatal(err)
	}
	pushScan(t, dir, 4, 1, nil) // unsigned, link intact
	return dir, root.Public().(ed25519.PublicKey)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantNoRows asserts the R10 shape: every status listed, a ledger Problem
// containing sub, and nothing counted.
func wantNoRows(t *testing.T, d ledger.Derived, sub string) {
	t.Helper()
	if !strings.Contains(d.Problem, sub) {
		t.Fatalf("Problem %q does not mention %q", d.Problem, sub)
	}
	if len(d.Scans)+len(d.Files)+len(d.Mutants)+len(d.Calls) != 0 || d.Reviews != 0 || len(d.Adjudications) != 0 {
		t.Fatalf("rows served from a broken ledger: %+v", d)
	}
	if strings.Contains(string(mustMarshal(t, d)), "null") {
		t.Fatal("null in output")
	}
}

func TestDeriveCountsOnlyVerifiedLive(t *testing.T) {
	dir, pub := mixedLedger(t)
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if d.Format != "corral-derive-1" || d.Problem != "" {
		t.Fatalf("format %q problem %q", d.Format, d.Problem)
	}
	var ver, uns int
	for _, e := range d.Entries {
		switch e.Status {
		case "verified":
			ver++
		case "unsigned":
			uns++
		default:
			t.Fatalf("unexpected status: %+v", e)
		}
	}
	if len(d.Entries) != 5 || ver != 4 || uns != 1 {
		t.Fatalf("verified/unsigned = %d/%d: %+v", ver, uns, d.Entries)
	}
	// The retracted scan (id 2) is verified but contributes nothing.
	if d.Entries[1].Status != "verified" {
		t.Fatalf("retracted scan status %+v", d.Entries[1])
	}
	var ids []int64
	for _, s := range d.Scans {
		ids = append(ids, s.ScanID)
	}
	if !reflect.DeepEqual(ids, []int64{1, 3}) {
		t.Fatalf("scans %v, want [1 3]", ids)
	}
}

// A flipped byte anywhere makes the ledger not intact: every status is listed,
// no row is served.
func TestDeriveTamperedEntryServesNoRows(t *testing.T) {
	dir, pub := mixedLedger(t)
	es := readBytes(t, dir)
	tamperFile(t, dir, es[2].File, `"Audited": 1`, `"Audited": 9`)
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 5 || d.Entries[2].Status != "failed" || d.Entries[2].Problem == "" {
		t.Fatalf("entries %+v", d.Entries)
	}
	wantNoRows(t, d, "does not verify")
}

// Attack (a): a flipped byte in the signed RETRACTION must not resurrect the
// scan it voided.
func TestDeriveTamperedRetractionServesNoRows(t *testing.T) {
	dir, pub := mixedLedger(t)
	es := readBytes(t, dir)
	tamperFile(t, dir, es[3].File, "wrong repo", "wrong rep0")
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if d.Entries[3].Status != "failed" {
		t.Fatalf("tampered retraction: %+v", d.Entries[3])
	}
	wantNoRows(t, d, "does not verify")
}

// Attack (b): an unsigned fork off scan 2's hash, equal length and longer than
// the real tail, must not displace real entries or resurrect retracted scans.
func TestDeriveUnsignedForkServesNoRows(t *testing.T) {
	for _, extra := range []int{1, 3} {
		dir, pub := mixedLedger(t)
		es := readBytes(t, dir) // s1 s2 s3 retract s4
		// Build the fork in a copy of the ledger truncated after scan 2, so its
		// entries carry Prev = scan 2's hash, then add them to the real dir.
		forkDir := t.TempDir()
		for _, e := range es[:2] {
			b, err := os.ReadFile(filepath.Join(dir, auditpush.ScansSubdir, e.File))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(forkDir, auditpush.ScansSubdir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(forkDir, auditpush.ScansSubdir, e.File), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < extra+2; i++ { // longer than or equal to s3, retract, s4 plus extra
			pushScan(t, forkDir, int64(100+i), 1, nil)
		}
		raw := rawDir(t, dir)
		have := map[string]bool{}
		for _, r := range raw {
			have[r.Name] = true
		}
		for _, r := range rawDir(t, forkDir) {
			if !have[r.Name] {
				raw = append(raw, r)
			}
		}
		d, err := ledger.Derive(raw, ledger.Trust{RootPub: pub})
		if err != nil {
			t.Fatal(err)
		}
		wantNoRows(t, d, "single chain")
		if !strings.Contains(d.Problem, "fork") {
			t.Fatalf("Problem should mention the fork: %q", d.Problem)
		}
	}
}

// R11: an unsigned retraction in an intact chain is honoured (it can only
// remove a number); an unsigned adjudication is ignored (it would assert one).
func TestDeriveUnsignedRetractionHonouredAdjudicationIgnored(t *testing.T) {
	root := key(0x43)
	rs := signerFor(root)
	pub := root.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	pushScan(t, dir, 1, 1, rs)
	pushScan(t, dir, 2, 1, rs)
	es := readBytes(t, dir)
	auditpush.SetLedgerSigner(nil)
	if _, err := auditpush.WriteRetraction(dir, es[0].Hash, "bad run", nil); err != nil {
		t.Fatal(err)
	}
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if d.Problem != "" || len(d.Scans) != 1 || d.Scans[0].ScanID != 2 {
		t.Fatalf("unsigned retraction not honoured: %+v", d)
	}
	if d.Entries[2].Status != "unsigned" {
		t.Fatalf("retraction status %+v", d.Entries[2])
	}
	// Unsigned adjudication of a signed review: ignored.
	if _, err := auditpush.WriteReview(dir, review.Review{
		Repo: "o/r", Commit: "deadbeef", Scope: "x", ReviewerModel: "m",
		Findings: []review.Finding{{ID: "R1", Claim: "c", Declared: "HYPOTHESIS", Tier: "HYPOTHESIS"}},
		Sound:    []string{"a", "b", "c"},
	}, rs); err != nil {
		t.Fatal(err)
	}
	es = readBytes(t, dir)
	rev := es[len(es)-1].Hash
	if _, err := auditpush.WriteAdjudication(dir, rev+"#R1", ledger.VerdictConfirmed, "p", "real", nil); err != nil {
		t.Fatal(err)
	}
	d, err = ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if d.Problem != "" || d.Reviews != 1 || len(d.Adjudications) != 0 {
		t.Fatalf("unsigned adjudication counted: %+v", d)
	}
}

func TestDeriveAgreesWithLiveEntries(t *testing.T) {
	dir, pub := mixedLedger(t)
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	checks, err := auditpush.VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	files, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var verified []ledger.Entry
	for i, c := range checks {
		if c.Problem == "" && c.Signed && c.SigOK {
			verified = append(verified, files[i])
		}
	}
	want := map[string]bool{}
	for _, e := range auditpush.ScanEntries(verified) {
		want[e.ScanUID] = true
	}
	got := map[string]bool{}
	for _, s := range d.Scans {
		got[s.ScanUID] = true
	}
	if len(want) != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("derive %v, LiveEntries %v", got, want)
	}
}

func TestDeriveOrderIndependent(t *testing.T) {
	dir, pub := mixedLedger(t)
	raw := rawDir(t, dir)
	base, err := ledger.Derive(raw, ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	want := mustMarshal(t, base)
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 20; i++ {
		sh := append([]ledger.RawEntry(nil), raw...)
		r.Shuffle(len(sh), func(a, b int) { sh[a], sh[b] = sh[b], sh[a] })
		d, err := ledger.Derive(sh, ledger.Trust{RootPub: pub})
		if err != nil {
			t.Fatal(err)
		}
		got := mustMarshal(t, d)
		if !bytes.Equal(got, want) {
			t.Fatalf("shuffle %d changed the output:\n%s\n%s", i, got, want)
		}
	}
}

func TestDeriveEmptyAndNeverNull(t *testing.T) {
	d, err := ledger.Derive(nil, ledger.Trust{})
	if err != nil {
		t.Fatal(err)
	}
	b := mustMarshal(t, d)
	if strings.Contains(string(b), "null") || d.Format != "corral-derive-1" {
		t.Fatalf("empty derive: %s", b)
	}
}

func TestDeriveNoRootKeyPassesNothingSigned(t *testing.T) {
	dir, _ := mixedLedger(t)
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Scans) != 0 {
		t.Fatalf("no root key, yet %d scans passed", len(d.Scans))
	}
	for _, e := range d.Entries {
		if e.Status == "verified" {
			t.Fatalf("verified without a root key: %+v", e)
		}
	}
	found := false
	for _, e := range d.Entries {
		if e.Status == "failed" && strings.Contains(e.Problem, "root key") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no problem names the missing root key: %+v", d.Entries)
	}
}

func TestDeriveOffChainAndUnparseableAndDuplicate(t *testing.T) {
	root := key(0x42)
	rs := signerFor(root)
	pub := root.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	pushScan(t, dir, 1, 1, rs)
	pushScan(t, dir, 2, 1, rs)
	// A fork: a second ledger's first entry has Prev "" and is a second head.
	other := t.TempDir()
	pushScan(t, other, 9, 1, rs)
	raw := rawDir(t, dir)
	for _, r := range rawDir(t, other) {
		r.Name = "fork-" + r.Name
		raw = append(raw, r)
	}
	raw = append(raw, ledger.RawEntry{Name: "junk.json", Bytes: []byte("not json")})
	d, err := ledger.Derive(raw, ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 4 || len(d.Scans) != 0 || d.Problem == "" {
		t.Fatalf("entries %+v scans %d", d.Entries, len(d.Scans))
	}
	for _, e := range d.Entries[2:] {
		if e.Status != "failed" {
			t.Fatalf("off-chain/unparseable not failed: %+v", e)
		}
	}
	if d.Entries[2].Problem != "not on the ledger's chain" || d.Entries[3].File != "junk.json" {
		t.Fatalf("tail %+v", d.Entries[2:])
	}
	if _, err := ledger.Derive([]ledger.RawEntry{raw[0], raw[0]}, ledger.Trust{}); err == nil {
		t.Fatal("two entries with one name must be an error")
	}
}

// The fixed seed is test-only: the fixture it signs is public data, and the
// key is never used for anything else.
func fixtureKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xC0}, ed25519.SeedSize))
}

func TestWriteTwoEntryFixture(t *testing.T) {
	fx := filepath.Join("testdata", "two-entries")
	k := fixtureKey()
	pub := k.Public().(ed25519.PublicKey)
	if os.Getenv("CORRAL_UPDATE_FIXTURES") == "1" {
		if err := os.RemoveAll(fx); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(fx, 0o755); err != nil {
			t.Fatal(err)
		}
		rs := signerFor(k)
		pushScan(t, fx, 1, 1, rs)
		pushScan(t, fx, 2, 1, rs)
		if err := os.WriteFile(filepath.Join(fx, "rootpub.hex"), []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hexPub, err := os.ReadFile(filepath.Join(fx, "rootpub.hex"))
	if err != nil {
		t.Fatalf("fixture missing (CORRAL_UPDATE_FIXTURES=1 writes it): %v", err)
	}
	got, err := hex.DecodeString(strings.TrimSpace(string(hexPub)))
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("rootpub.hex is not the fixed test key's public key")
	}
	raw := rawDir(t, fx)
	sort.Slice(raw, func(i, j int) bool { return raw[i].Name < raw[j].Name })
	d, err := ledger.Derive(raw, ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Scans) != 2 || len(d.Entries) != 2 || d.Entries[0].Status != "verified" || d.Entries[1].Status != "verified" {
		t.Fatalf("fixture derives to %+v", d)
	}
}

// Derive carries Verify's Signer and Note into each status, so the page can
// say who signed and what a retraction did without re-deriving either.
func TestDeriveCarriesSignerAndNote(t *testing.T) {
	fx := filepath.Join("testdata", "two-entries")
	pub := fixtureKey().Public().(ed25519.PublicKey)
	d, err := ledger.Derive(rawDir(t, fx), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range d.Entries {
		if st.Signer != "root" {
			t.Fatalf("signer not carried: %+v", st)
		}
	}
	dir, pub := mixedLedger(t)
	d, err = ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, st := range d.Entries {
		if st.Note != "" {
			notes++
		}
	}
	if notes == 0 {
		t.Fatalf("no note carried from a ledger whose checks carry notes: %+v", d.Entries)
	}
}
