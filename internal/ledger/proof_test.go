// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"testing"
	"time"
)

type pOpts struct {
	hash       string
	repo       string
	identity   string
	gen        string
	runner     string
	runURL     string
	dirty      bool
	dirtyNil   bool // TreeDirty not recorded
	unsigned   bool // Signed=false: the entry carries no signature
	noSig      bool // SigChecked=false
	noCheck    bool
	started    time.Time
	files      []Row
	kind       string
	retractsOf string
}

func proofEntry(t *testing.T, o pOpts) (Entry, ChainCheck) {
	t.Helper()
	if o.hash == "" {
		o.hash = "h1"
	}
	if o.repo == "" {
		o.repo = "https://github.com/acme/widgets"
	}
	if o.identity == "" {
		o.identity = "git"
	}
	if o.gen == "" {
		o.gen = "40"
	}
	if o.runner == "" {
		o.runner = "go-test"
	}
	if o.started.IsZero() {
		o.started = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	}
	if o.files == nil {
		o.files = []Row{{Lang: "go", Path: "a.go", Audited: 1, MutantsGraded: 5}}
	}
	d := o.dirty
	st := o.started
	e := Entry{Kind: o.kind, Hash: o.hash, Retracts: o.retractsOf}
	e.Bundle.Scan = ScanRow{Repo: o.repo, RepoIdentity: o.identity, Commit: "c1",
		RunURL: o.runURL, Runner: o.runner, VerdictGeneration: o.gen, TreeDirty: &d, StartedAt: &st}
	if o.dirtyNil {
		e.Bundle.Scan.TreeDirty = nil
	}
	e.Bundle.Files = o.files
	sigChecked := !o.noSig && !o.unsigned
	c := ChainCheck{Hash: o.hash, HashOK: true, LinkOK: true, Signed: !o.unsigned,
		SigChecked: sigChecked, SigOK: sigChecked, Signer: "root"}
	if o.unsigned {
		c.Signer = ""
	}
	return e, c
}

var testSelf = []string{"github.com/pdbethke/corralai"}
var testRunners = map[string][]string{"go-test": {"go"}}

func proofs(t *testing.T, es []Entry, cs []ChainCheck) map[string]LanguageProof {
	t.Helper()
	m, err := LanguageProofs(es, cs, testSelf, "40", testRunners)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func one(t *testing.T, o pOpts) map[string]LanguageProof {
	e, c := proofEntry(t, o)
	cs := []ChainCheck{c}
	if o.noCheck {
		cs = nil
	}
	return proofs(t, []Entry{e}, cs)
}

func TestLanguageProofsCountsAQualifyingLocalRun(t *testing.T) {
	two := 2
	m := one(t, pOpts{files: []Row{{Lang: "go", Audited: 1, MutantsGraded: 3, ProvenMissed: two}}})
	g := m["go"]
	if g.Local == nil || g.Local.Hash != "h1" || g.CI != nil || !g.ProvedAGap || g.Local.Runner != "go-test" {
		t.Fatalf("got %+v", g)
	}
}

func TestLanguageProofsRunURLMeansCI(t *testing.T) {
	g := one(t, pOpts{runURL: "https://ci/1"})["go"]
	if g.CI == nil || g.Local != nil {
		t.Fatalf("got %+v", g)
	}
}

func TestLanguageProofsUncheckedSignatureNeverCounts(t *testing.T) {
	g := one(t, pOpts{noSig: true})["go"]
	if g.Local != nil || g.Unchecked != 1 || g.BelowFloor != 0 {
		t.Fatalf("got %+v", g)
	}
}

func TestLanguageProofsMissingCheckNeverCounts(t *testing.T) {
	if m := one(t, pOpts{noCheck: true}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsBadSignatureCountsNowhere(t *testing.T) {
	e, c := proofEntry(t, pOpts{})
	c.SigOK = false
	if m := proofs(t, []Entry{e}, []ChainCheck{c}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsSelfAuditExcluded(t *testing.T) {
	if m := one(t, pOpts{repo: "git@github.com:PdBethke/corralai.git"}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestNormalizeRepoSpellings(t *testing.T) {
	for _, s := range []string{
		"https://github.com/pdbethke/corralai",
		"https://github.com/PdBethke/corralai.git",
		"git@github.com:pdbethke/corralai.git",
		"github.com/pdbethke/corralai/",
		"ssh://git@github.com/pdbethke/corralai.git",
		"ssh://git@github.com:22/pdbethke/corralai.git",
	} {
		if got := NormalizeRepo(s); got != "github.com/pdbethke/corralai" {
			t.Errorf("%q -> %q", s, got)
		}
	}
	// scp form has no port: an all-digit owner stays an owner.
	if got := NormalizeRepo("git@github.com:123/name.git"); got != "github.com/123/name" {
		t.Errorf("scp numeric owner -> %q", got)
	}
}

func TestLanguageProofsRequiresGeneration(t *testing.T) {
	e, c := proofEntry(t, pOpts{})
	e.Bundle.Scan.VerdictGeneration = ""
	if m := proofs(t, []Entry{e}, []ChainCheck{c}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsBelowFloor(t *testing.T) {
	g := one(t, pOpts{gen: "39"})["go"]
	if g.Local != nil || g.BelowFloor != 1 {
		t.Fatalf("got %+v", g)
	}
}

func TestLanguageProofsNonNumericGeneration(t *testing.T) {
	if m := one(t, pOpts{gen: "40a"}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsRequiresRunner(t *testing.T) {
	e, c := proofEntry(t, pOpts{})
	e.Bundle.Scan.Runner = ""
	if m := proofs(t, []Entry{e}, []ChainCheck{c}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsDemoAndOperatorIdentityExcluded(t *testing.T) {
	for _, id := range []string{"none", "operator"} {
		if m := one(t, pOpts{identity: id}); len(m) != 0 {
			t.Errorf("%s: %+v", id, m)
		}
	}
	e, c := proofEntry(t, pOpts{})
	e.Bundle.Scan.Repo = ""
	if m := proofs(t, []Entry{e}, []ChainCheck{c}); len(m) != 0 {
		t.Fatalf("empty repo: %+v", m)
	}
}

func TestLanguageProofsDirtyTreeExcluded(t *testing.T) {
	if m := one(t, pOpts{dirty: true}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

// A run whose tree cleanliness was not recorded (nil: every pull_request-
// triggered Action run, which grades the merge ref, not HEAD) is not a proof:
// the spec requires TreeDirty recorded AND false.
func TestLanguageProofsUnknownCleanlinessExcluded(t *testing.T) {
	if m := one(t, pOpts{dirtyNil: true}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsPerFileNotPerScan(t *testing.T) {
	m := one(t, pOpts{files: []Row{
		{Lang: "go", Audited: 1, MutantsGraded: 2},
		{Lang: "python", Audited: 1, BaselineFailed: true, MutantsGraded: 2},
		{Lang: "ruby", Audited: 1, MutantsGraded: 0},
		{Lang: "php", Audited: 0, MutantsGraded: 2},
	}})
	if m["go"].Local == nil || len(m) != 1 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsCacheHitAndReusedExcluded(t *testing.T) {
	id := int64(3)
	m := one(t, pOpts{files: []Row{
		{Lang: "go", Audited: 1, MutantsGraded: 2, CacheHit: true},
		{Lang: "python", Audited: 1, MutantsGraded: 2, ReusedFromScanID: &id},
	}})
	if len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsRetractedFallsBack(t *testing.T) {
	old := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e1, c1 := proofEntry(t, pOpts{hash: "old", started: old})
	e2, c2 := proofEntry(t, pOpts{hash: "new", started: old.Add(time.Hour)})
	r := Entry{Kind: KindRetract, Hash: "r", Retracts: "new"}
	m := proofs(t, []Entry{e1, e2, r}, []ChainCheck{c1, c2})
	if m["go"].Local == nil || m["go"].Local.Hash != "old" {
		t.Fatalf("got %+v", m["go"].Local)
	}
}

func TestLanguageProofsNewestWinsTieByChainOrder(t *testing.T) {
	ts := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e1, c1 := proofEntry(t, pOpts{hash: "a", started: ts})
	e2, c2 := proofEntry(t, pOpts{hash: "b", started: ts})
	m := proofs(t, []Entry{e1, e2}, []ChainCheck{c1, c2})
	if m["go"].Local.Hash != "b" {
		t.Fatalf("got %+v", m["go"].Local)
	}
}

func TestLanguageProofsReviewsOnlyLedgerIsEmpty(t *testing.T) {
	r := Entry{Kind: KindRetract, Hash: "r", Retracts: "x"}
	if m := proofs(t, []Entry{r}, nil); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

// A mixed-language entry proves a language only through a runner that runs
// it: the go file of a go-test entry qualifies, its python file does not.
func TestLanguageProofsRunnerShownOnlyForItsLanguage(t *testing.T) {
	m := one(t, pOpts{files: []Row{
		{Lang: "go", Audited: 1, MutantsGraded: 1},
		{Lang: "python", Audited: 1, MutantsGraded: 1},
	}})
	if m["go"].Local == nil || m["go"].Local.Runner != "go-test" {
		t.Fatalf("go: %+v", m["go"])
	}
	if _, ok := m["python"]; ok {
		t.Fatalf("python proved by a go runner: %+v", m["python"])
	}
}

func TestLanguageProofsRunnerMustRunTheLanguage(t *testing.T) {
	langs := map[string][]string{"pytest": {"python"}}
	run := func(lang string) map[string]LanguageProof {
		e, c := proofEntry(t, pOpts{runner: "pytest", files: []Row{{Lang: lang, Audited: 1, MutantsGraded: 1}}})
		m, err := LanguageProofs([]Entry{e}, []ChainCheck{c}, testSelf, "40", langs)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, ok := run("go")["go"]; ok {
		t.Fatalf("go proved by pytest")
	}
	py := run("python")["python"]
	if py.Local == nil || py.Local.Runner != "pytest" {
		t.Fatalf("python: %+v", py)
	}
}

func TestLanguageProofsRefusesEmptySelf(t *testing.T) {
	for _, self := range [][]string{nil, {"", "  "}} {
		if _, err := LanguageProofs(nil, nil, self, "40", nil); err == nil {
			t.Errorf("self %q accepted", self)
		}
	}
}

func TestLanguageProofsRefusesNonNumericFloor(t *testing.T) {
	if _, err := LanguageProofs(nil, nil, testSelf, "4x", nil); err == nil {
		t.Fatal("accepted")
	}
}

func TestLanguageProofsEachCheckConditionRefuses(t *testing.T) {
	for name, mut := range map[string]func(*ChainCheck){
		"hash":    func(c *ChainCheck) { c.HashOK = false },
		"link":    func(c *ChainCheck) { c.LinkOK = false },
		"problem": func(c *ChainCheck) { c.Problem = "bad" },
		"signer":  func(c *ChainCheck) { c.Signer = "" },
	} {
		e, c := proofEntry(t, pOpts{})
		mut(&c)
		if m := proofs(t, []Entry{e}, []ChainCheck{c}); len(m) != 0 {
			t.Errorf("%s: %+v", name, m)
		}
	}
}

func TestLanguageProofsCheckMatchedByHashNotPosition(t *testing.T) {
	e, c := proofEntry(t, pOpts{hash: "x"})
	c.Hash = "other"
	if m := proofs(t, []Entry{e}, []ChainCheck{c}); len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsEmptyLangIgnored(t *testing.T) {
	m := one(t, pOpts{files: []Row{{Lang: "", Audited: 1, MutantsGraded: 1}}})
	if len(m) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLanguageProofsNewestByTimeNotChainOrder(t *testing.T) {
	ts := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e1, c1 := proofEntry(t, pOpts{hash: "newer", started: ts.Add(time.Hour)})
	e2, c2 := proofEntry(t, pOpts{hash: "older", started: ts})
	m := proofs(t, []Entry{e1, e2}, []ChainCheck{c1, c2})
	if m["go"].Local.Hash != "newer" {
		t.Fatalf("got %+v", m["go"].Local)
	}
}

func TestLanguageProofsUncheckedOutranksBelowFloorInTheRule(t *testing.T) {
	g := one(t, pOpts{gen: "39", noSig: true})["go"]
	if g.BelowFloor != 0 || g.Unchecked != 1 || g.Local != nil {
		t.Fatalf("got %+v", g)
	}
}

// An entry that carries no signature is Unsigned, never Unchecked: no key
// the reader could supply would change it.
func TestLanguageProofsUnsignedCountsAsUnsignedNotUnchecked(t *testing.T) {
	g := one(t, pOpts{unsigned: true})["go"]
	if g.Local != nil || g.Unsigned != 1 || g.Unchecked != 0 || g.BelowFloor != 0 {
		t.Fatalf("got %+v", g)
	}
}

func TestLanguageProofsUnsignedOutranksBelowFloorInTheRule(t *testing.T) {
	g := one(t, pOpts{gen: "39", unsigned: true})["go"]
	if g.Local != nil || g.Unsigned != 1 || g.Unchecked != 0 || g.BelowFloor != 0 {
		t.Fatalf("got %+v", g)
	}
}
