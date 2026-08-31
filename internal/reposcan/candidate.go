// SPDX-License-Identifier: Elastic-2.0

// Package reposcan fans corral's single-file adequacy audit out over a whole
// repository: enumerate candidates, emit owner-keyed jobs, run them through an
// Executor, aggregate the verdicts into one report with complete accounting.
package reposcan

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
)

// Candidate is one source file that can be audited, paired with the test
// file that is supposed to be exercising it.
type Candidate struct {
	Path     string
	TestPath string
	Lang     string
	// ViaSearch is true when TestPath was found by the recursive fallback
	// (findInUniverse's second stage) rather than by the plugin's own
	// ordered TestPaths candidates — a test that EXISTS but that no
	// filename convention predicted. Carried through unchanged whenever a
	// Candidate is copied (demoteAmbiguousPairings, --diff/--top
	// selection), so a later reader (the JSON inventory, the human report)
	// can disclose it without re-deriving the fact.
	ViaSearch bool
	// CoveringTestPath is the selection evidence's most-covering test FILE
	// for this candidate — the covering test with the most executed lines of
	// this file, by containing file (ties: the more specific path — see
	// moreSpecificTestPath). Set ONLY for an evidence-only candidate
	// (TestPath == ""), where it is the authored-test landing hint: the
	// pairing-based candidate already has an obvious home (TestPath's own
	// directory), and this is the measured proxy for "where this file's
	// tests live" when no filename pairing exists. "" for a paired
	// candidate — WidenCandidacyByEvidence never sets it there, so a
	// mirrored fixture's already-candidate rows stay byte-identical.
	CoveringTestPath string
	// CoveringTests is the number of tests the evidence showed execute this
	// file, disclosed on the evidence-paired report line and carried to the
	// ledger's covering_tests column. nil means the evidence never measured
	// this file (including every candidate on a scan where no evidence ran
	// at all) — never confused with a measured, genuine zero, which
	// WidenCandidacyByEvidence excludes as ReasonUncovered rather than
	// leaving as a zero-value Candidate.
	CoveringTests *int
}

// Exclusion is a file deliberately NOT audited, with a machine-stable reason.
// Exclusions are reported, never dropped: the repo report must account for
// every file so a reader can see what the score does and does not cover.
type Exclusion struct {
	Path   string
	Reason string
}

// Exclusion reasons.
const (
	ReasonNoLanguage   = "no-language"
	ReasonIsTest       = "is-test"
	ReasonNoPairedTest = "no-paired-test"
	// ReasonAmbiguousTest marks a source file whose resolved test path is
	// ALSO claimed by at least one other source file at the same or better
	// specificity rank. Ordered TestPaths candidates broke the injectivity
	// the old single-path design had for free (one source, one test, no two
	// sources could ever name the same test) — this reason is the repair:
	// a wrong pairing plants mutants in one file and grades them against a
	// DIFFERENT file's tests, producing a confident, signed, wrong adequacy
	// verdict. An accounted non-pairing is always the safer failure. See
	// demoteAmbiguousPairings.
	ReasonAmbiguousTest = "ambiguous-test"
	// ReasonNotRegularFile covers symlinks, FIFOs, sockets and devices. A
	// symlink is the dangerous one: `secrets.py -> ~/.aws/credentials` in a
	// cloned repo would otherwise be auto-discovered, digested, shipped to a
	// model provider and copied into the jail workspace. The rest simply
	// cannot be audited (a FIFO read blocks forever). Fail closed: they are
	// accounted for, never followed.
	ReasonNotRegularFile = "not-a-regular-file"
	// ReasonSkippedDir marks a file inside a directory the walk does not
	// descend (build output: dist, build, target, .tox, site-packages). It is
	// still ACCOUNTED: a reader must be able to see the scan chose not to look
	// there, rather than see a repo that appears smaller than it is. VCS and
	// dependency trees are the exception — see invisibleDirs.
	ReasonSkippedDir = "skipped-dir"
	// ReasonGitignored marks a file the repository's own .gitignore says is
	// not its source — a sibling worktree, generated output, a scratch copy.
	// The hardcoded skip list cannot know a project's private names, and a
	// gitignored COPY of a source file pairs perfectly with its own test, so
	// without this it is counted (and can be selected) as a candidate: on
	// this repo three stale worktrees under .worktrees/ turned 227 candidates
	// into 468. Accounted like skipped-dir: the file is listed, never dropped.
	ReasonGitignored = "gitignored"
	// ReasonUncovered marks a source file the selection evidence actually
	// MEASURED and found zero tests executing, AND found no coverage for it
	// at all outside a test either — genuinely nothing executed it. The
	// loudest finding a mutation audit of a test suite can produce, and a
	// different claim from ReasonNoPairedTest (a statement about NAMES: no
	// filename convention predicted a test, which says nothing about
	// whether some other test happens to execute the file). Applied ONLY
	// when evidence exists, positively measured the file at zero covering
	// tests, AND found no import-time (static) coverage for it either — see
	// WidenCandidacyByEvidence and ReasonImportOnly, the sibling finding
	// for a file executed but never by a test. Absence of evidence is
	// never treated as evidence of absence: a file the evidence never
	// measured keeps ReasonNoPairedTest. The exact string is load-bearing —
	// it is the disclosure text itself, not a machine code a reader has to
	// translate.
	ReasonUncovered = "uncovered — no test executes this file"
	// ReasonImportOnly marks a source file the selection evidence
	// positively measured at zero covering TESTS but non-empty coverage
	// OUTSIDE any test context — executed only at import/module-load time
	// (a package __init__.py, a module-level constant, a decorator
	// evaluated at import). This is NOT ReasonUncovered: the file was
	// genuinely executed, coverage.py recorded real lines for it — it is
	// only that no TEST exercises it directly, which pytest-cov's own
	// per-test contexts cannot attribute to any test id (see
	// lang.FileCoverage.HasStatic). Calling this "uncovered" would be
	// false in the sense a reader checks it against: every test in the
	// suite typically imports the package, which is exactly why this hits
	// on essentially every Python repo's __init__.py and constants
	// modules. Same disposition as ReasonUncovered — excluded from the
	// audit, since there is nothing a TEST-scoped kill rate could grade it
	// against — but a different, honest claim, counted separately. The
	// exact string is load-bearing, same as ReasonUncovered's.
	ReasonImportOnly = "imported at load time — no test exercises it directly"
	// ReasonNoExecutableCode marks a source file the selection evidence
	// positively measured with ZERO covering tests AND zero coverage
	// outside a test either — the SAME shape ReasonUncovered reads — but
	// whose file coverage.py's own static parse found to contain NO
	// executable statement at all (see lang.FileCoverage.HasStatements): a
	// genuinely empty file, or one that is comment-only. There is nothing
	// to execute, nothing a test could cover, and nothing a test could be
	// blamed for missing — a 0-byte tests/__init__.py is the textbook
	// case, and every real Python repo carries several such files. Calling
	// this "uncovered" would be a nonsense claim (there IS no test-vs-code
	// question to answer) that inflates the headline finding on literally
	// every scan. Benign: still excluded (there is nothing to grade), but
	// never counted alongside ReasonUncovered/ReasonImportOnly, and never
	// worded as though the tests failed at anything.
	ReasonNoExecutableCode = "no executable code"
)

// skipDirs are never walked: dependency, build-output and VCS trees are not
// the subject of an audit of THIS repo's tests, and letting them into the walk
// puts vendored third-party code into the report's denominator.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, ".bundle": true, "testdata": true,
	"dist": true, "build": true, "target": true, ".tox": true,
	"site-packages": true,
}

// invisibleDirs are skipped WITHOUT accounting. Two kinds of tree qualify,
// for the same reason:
//
//   - .git is not source at all — listing its objects would swamp the report.
//   - DEPENDENCY trees are not THIS repo's source either; they are
//     third-party code the audit has no business grading, and they are
//     enormous. node_modules alone is routinely tens of thousands of files;
//     a Python virtualenv's site-packages is comparable. Enumerating them
//     buries the handful of entries a reader actually needs in a report that
//     gets signed and anchored.
//
// BUILD OUTPUT (dist, build, target) stays ACCOUNTED: those trees are small
// and derived from this repo, so a reader benefits from seeing the scan chose
// not to look there.
//
// .tox is classified as a dependency tree rather than build output because a
// tox environment contains full virtualenvs — it is site-packages wearing a
// different name, not a modest build artifact.
var invisibleDirs = map[string]bool{
	".git": true,
	// Dependency trees, by ecosystem: node, go, python (×3), ruby.
	"node_modules": true, "vendor": true,
	".venv": true, "venv": true, "site-packages": true, ".tox": true,
	".bundle": true,
}

// subtreeFiles enumerates the regular files under dir as exclusions carrying
// reason, keyed repo-relative to root. Non-regular entries (symlinks, devices)
// are not followed and not listed: the point is an honest count of source
// files not looked at, not an inventory of the filesystem. A file named .git
// (a linked worktree's pointer to its main repository) is VCS, not source,
// and stays invisible like the .git directory it stands in for.
//
// Degradation: a subtree that cannot be read (permissions, a directory that
// vanished mid-scan) is NOT scan-fatal. These are trees the audit deliberately
// does not look at — before this accounting existed the walk never descended
// into them at all, so an unreadable build/ could not affect a scan. Failing
// the whole run over one would be a regression. The unreadable subtree is
// recorded as a single skipped-dir entry and the walk continues.
func subtreeFiles(dir, root, reason string) ([]Exclusion, error) {
	var out []Exclusion
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Account the unreadable path as one entry and move on. d may be
			// nil here (an error stat-ing the root of the subtree).
			if rel, rerr := filepath.Rel(root, p); rerr == nil {
				out = append(out, Exclusion{Path: filepath.ToSlash(rel), Reason: reason})
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// A dependency tree NESTED inside an accounted one is still
			// invisible: build/node_modules is no more worth enumerating
			// than a top-level node_modules, and it is just as large.
			// Without this, accounting build/ drags its whole dependency
			// tree into a signed report.
			if p != dir && invisibleDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == ".git" {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, Exclusion{Path: filepath.ToSlash(rel), Reason: reason})
		return nil
	})
	return out, err
}

// gitIgnored asks the repository what its own .gitignore stack excludes under
// root, as root-relative slash paths: files, and directories git collapsed to
// a single entry (a wholly-ignored directory, or a nested repository such as
// a linked worktree, comes back as "dir/" with nothing beneath it listed).
//
// The scan does not reimplement ignore semantics — nested .gitignore files,
// .git/info/exclude, core.excludesFile and negations all already have an
// authority, and it is git. Outside a git work tree, or with no git on PATH,
// both sets are nil and the walk behaves exactly as it always has: there is
// no authority to consult, so .gitignore is just a file. Inside a work tree a
// git failure IS an error — degrading to "walk everything" there would
// silently move the candidate count, which is the defect this exists to fix.
func gitIgnored(root string) (files, dirs map[string]bool, err error) {
	git, lerr := exec.LookPath("git")
	if lerr != nil {
		return nil, nil, nil
	}
	probe := exec.Command(git, "-C", root, "rev-parse", "--is-inside-work-tree") // #nosec G204 -- git resolved via LookPath; root is the operator's own scan root; every other arg literal
	if out, perr := probe.Output(); perr != nil || strings.TrimSpace(string(out)) != "true" {
		var exit *exec.ExitError
		if perr != nil && !errors.As(perr, &exit) {
			return nil, nil, fmt.Errorf("git rev-parse: %w", perr)
		}
		return nil, nil, nil
	}
	ls := exec.Command(git, "-C", root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory") // #nosec G204 -- same: LookPath binary, operator's root, literal args
	out, lserr := ls.Output()
	if lserr != nil {
		return nil, nil, fmt.Errorf("git ls-files --ignored in %s: %w", root, lserr)
	}
	files, dirs = map[string]bool{}, map[string]bool{}
	for _, ent := range bytes.Split(out, []byte{0}) {
		if len(ent) == 0 {
			continue
		}
		p := filepath.ToSlash(string(ent))
		if strings.HasSuffix(p, "/") {
			dirs[strings.TrimSuffix(p, "/")] = true
		} else {
			files[p] = true
		}
	}
	return files, dirs, nil
}

// Enumerate walks root and classifies every file into an audit candidate or
// an exclusion with a reason. Results are sorted by path so a scan of the
// same tree always produces the same job order.
func Enumerate(root string) ([]Candidate, []Exclusion, error) {
	return EnumerateWithTests(root, nil)
}

// EnumerateWithTests is Enumerate with a tenant-supplied source→test mapping
// consulted BEFORE convention. See TestMap for why convention alone cannot pair
// every repository. A nil map is exactly Enumerate.
func EnumerateWithTests(root string, tests *TestMap) ([]Candidate, []Exclusion, error) {
	var cands []Candidate
	var excl []Exclusion

	ignoredFiles, ignoredDirs, err := gitIgnored(root)
	if err != nil {
		return nil, nil, err
	}

	// First pass: which repo-relative paths exist, so pairing can check.
	present := map[string]bool{}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				// Account the files we are choosing not to look at. Without
				// this they vanish from the walked total, making the repo
				// look smaller than it is in a report that gets signed.
				if !invisibleDirs[d.Name()] {
					sub, serr := subtreeFiles(path, root, ReasonSkippedDir)
					if serr != nil {
						return serr
					}
					excl = append(excl, sub...)
				}
				return filepath.SkipDir
			}
			if ignoredDirs[filepath.ToSlash(rel)] {
				// The repo's own .gitignore says nothing under here is its
				// source. Accounted like build output, for the same reason.
				sub, serr := subtreeFiles(path, root, ReasonGitignored)
				if serr != nil {
					return serr
				}
				excl = append(excl, sub...)
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(rel)
		// ONLY regular files are auditable. Everything else — above all a
		// symlink, which can point anywhere on the host — is excluded with a
		// reason instead of being enumerated. The scan AUTO-DISCOVERS its
		// subjects (unlike `certify --local`, where the operator names the
		// file), so following a link here would be a repository choosing what
		// the audit reads off the operator's disk.
		if !d.Type().IsRegular() {
			excl = append(excl, Exclusion{Path: slash, Reason: ReasonNotRegularFile})
			return nil
		}
		if ignoredFiles[slash] {
			excl = append(excl, Exclusion{Path: slash, Reason: ReasonGitignored})
			return nil
		}
		present[slash] = true
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, nil, err
	}

	// rank tracks, per candidate (parallel to cands, before the ambiguity
	// pass below), the EVIDENTIARY specificity of the match — each plugin's
	// own TestCandidate.Rank, NOT the position at which it was found in the
	// (deduped) TestPaths list. Position and rank can diverge: for a shallow
	// source several differently-specific forms can collapse onto the same
	// string, and dedupeCandidates attributes that surviving entry the LEAST
	// specific of the colliding forms' ranks — see lang.TestCandidate for why
	// using position instead let two equally-uninformative matches from
	// different-depth sources dodge the ambiguity check entirely. rank only
	// exists to resolve cross-source collisions below and is discarded once
	// that pass is done — Candidate itself carries no notion of rank.
	var rank []int
	// explicitPairs is parallel to cands: true where the pairing came from the
	// tenant's map rather than convention. Those are exempt from ambiguity
	// demotion — a deliberate many-to-one mapping is not an accidental
	// collision (see demoteAmbiguousPairings).
	var explicitPairs []bool

	// presentList is present's keys as a slice, computed ONCE for the whole
	// scan: findInUniverse's recursive-search fallback needs to range over
	// the visible file set, and a fresh git ls-files (or map-to-slice) per
	// source file would turn an O(files) walk into O(files²). Every entry
	// here already passed the same gitignore/skipDir gate `present` did, so
	// the recursive fallback can never reach a directory the walk above
	// itself refused to enter.
	presentList := make([]string, 0, len(present))
	for rel := range present {
		presentList = append(presentList, rel)
	}

	for rel := range present {
		p, ok := lang.Detect(rel)
		if !ok {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonNoLanguage})
			continue
		}
		// A file that IS the sibling test of some source file is not itself
		// a subject. Detected structurally: its own conventional test path
		// differs from itself only for non-test files.
		if isTestFile(p, rel) {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonIsTest})
			continue
		}
		// Walk the plugin's ordered candidates and pair with the first one
		// that actually exists in this repo. The list is ordered most
		// specific first (see each plugin's TestPaths), so a sibling or
		// full-directory-mirror test wins over a same-named test that could
		// plausibly belong to a different source file.
		tp := ""
		tpRank := -1
		explicit := false
		viaSearch := false

		// The tenant's mapping wins: they know their layout, and corral cannot
		// infer a project's own naming shorthand (see TestMap).
		if mapped, ok := tests.TestFor(rel); ok {
			if !present[mapped] {
				// REFUSED, never a silent fallback to convention: falling back
				// would pair the file to something the operator did not choose,
				// and they would have no way to see their mapping was ignored.
				excl = append(excl, Exclusion{Path: rel, Reason: ReasonMappedTestMissing})
				continue
			}
			tp, tpRank, explicit = mapped, 0, true
		} else {
			// The plugin's own ordered candidates first — a test that EXISTS
			// under the exact convention TestPaths predicts keeps absolute
			// priority, unchanged from before findInUniverse existed. Only
			// when NONE of them exists does the recursive fallback run (see
			// findInUniverse and lang.TestRooter): a test that exists but
			// that no filename convention predicted still beats no pairing
			// at all.
			tp, tpRank, viaSearch = findInUniverse(p, rel, present, presentList)
		}
		if tp == "" {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonNoPairedTest})
			continue
		}
		cands = append(cands, Candidate{Path: rel, TestPath: tp, Lang: p.Name(), ViaSearch: viaSearch})
		rank = append(rank, tpRank)
		explicitPairs = append(explicitPairs, explicit)
	}

	cands, excl = demoteAmbiguousPairings(cands, rank, excl, explicitPairs)

	sort.Slice(cands, func(i, j int) bool { return cands[i].Path < cands[j].Path })
	sort.Slice(excl, func(i, j int) bool { return excl[i].Path < excl[j].Path })
	return cands, excl, nil
}

// WidenCandidacyByEvidence is the evidence-first half of candidacy: a file
// with a language, not gitignored, not a test — and NO filename pairing —
// still becomes a candidate when the selection evidence shows at least one
// test executes it, and is relabeled when the evidence positively measured
// it at ZERO covering tests (rather than left as ReasonNoPairedTest) — as
// ReasonUncovered when NOTHING executed it at all, or as ReasonImportOnly
// when it WAS executed, just never by a test directly (import/module-load
// time coverage only — a package __init__.py, a module constant). These
// are two different, honest claims under what the design called one
// "zero covering tests" state, and conflating them would call a package's
// __init__.py "uncovered" on essentially every Python repo, since every
// test typically imports it. Candidacy is therefore paired ∪
// evidence-covered: pairing alone (stranger-path's own walk) is untouched,
// and this only ever ADDS candidates or renames the reason on an exclusion
// — it never removes a pairing-based candidate or changes its
// TestPath/ViaSearch.
//
// ok mirrors ParseEvidenceIndex's own bool: false means there is no index to
// widen with (evidence never ran, an unsupported language, or unparseable
// evidence), and cands/excl come back byte-identical to what was passed in —
// the pairing-only candidacy the design calls the fallback.
//
// Already-paired candidates get CoveringTests filled in when the evidence
// also measured them (ledger metadata only — see Candidate.CoveringTests);
// TestPath, ViaSearch and every other field, and every excluded-for-another-
// reason entry, are left exactly as the pairing walk produced them, which is
// what keeps an already-candidate file's report line, grading command, cache
// key and verdict byte-identical (see the mirrored-fixture test).
//
// promoted is the count of NEW candidates this call added (the third
// return) — a caller keeping its own Enumerate-level exclusion count needs
// it to decrement that count by exactly the same number a promotion moves
// out of excl, rather than re-deriving "how many enumerate-level
// exclusions are left" from a excl slice that may since have grown
// candidate-level entries of its own (not-selected, ungoaled, ...) that
// this function correctly ignores but a naive re-count would not.
func WidenCandidacyByEvidence(cands []Candidate, excl []Exclusion, idx EvidenceIndex, ok bool) ([]Candidate, []Exclusion, int) {
	if !ok {
		return cands, excl, 0
	}

	for i := range cands {
		if n, _, _, _, measured := idx.CoverageFor(cands[i].Path); measured {
			v := n
			cands[i].CoveringTests = &v
		}
	}

	// hasPath is lang.LibraryCodeClassifier's file-existence oracle: the
	// repo's own enumerated inventory (every candidate AND every exclusion
	// — cands/excl together are every source Enumerate ever saw) UNIONED
	// with whatever the evidence itself measured, so a package __init__.py
	// is found whether it was paired, excluded, or simply present in the
	// coverage data — never a second filesystem read.
	known := make(map[string]bool, len(cands)+len(excl))
	for _, c := range cands {
		known[c.Path] = true
	}
	for _, e := range excl {
		known[e.Path] = true
	}
	hasPath := func(p string) bool { return known[p] || idx.Measured(p) }

	kept := excl[:0:0]
	var promoted int
	for _, e := range excl {
		if e.Reason != ReasonNoPairedTest {
			kept = append(kept, e)
			continue
		}
		n, mostCovering, hasStatic, hasStatements, measured := idx.CoverageFor(e.Path)
		switch {
		case !measured:
			// Absence of evidence is not evidence of absence: this file's
			// only honest reason remains "no filename convention predicted a
			// test", not a claim the evidence never actually made.
			kept = append(kept, e)
		case n > 0:
			v := n
			langName := ""
			if p, ok := lang.Detect(e.Path); ok {
				langName = p.Name()
			}
			cands = append(cands, Candidate{
				Path:             e.Path,
				Lang:             langName,
				CoveringTestPath: mostCovering,
				CoveringTests:    &v,
			})
			promoted++
		case !isLibraryCode(e.Path, hasPath):
			// Founder ruling: uncovered/import-only/no-executable-code are
			// the loudest findings a mutation audit produces, and they must
			// speak only about the library a repo SHIPS — docs config, a
			// setup/build script, a one-off automation script all measure
			// real, but the finding would dilute the headline on every real
			// repo, which carries several. The file is still ENUMERATED and
			// still excluded, under its ORIGINAL, honest reason — this is
			// promotion's OWN gate too (n > 0, above), never invoked for a
			// non-library file, so a covered non-library file still becomes
			// a candidate; only the negative claims are scoped.
			kept = append(kept, e)
		case !hasStatements:
			// Coverage's own static parse found NOTHING to execute — an
			// empty or comment-only file (a 0-byte __init__.py is the
			// textbook case). There is no code for a test to have covered,
			// so this is benign, not a scolding "your tests are bad" claim.
			kept = append(kept, Exclusion{Path: e.Path, Reason: ReasonNoExecutableCode})
		case hasStatic:
			// Executed, just never by a test directly (import/module-load
			// time only) — a real, different finding from ReasonUncovered.
			// See ReasonImportOnly's own doc.
			kept = append(kept, Exclusion{Path: e.Path, Reason: ReasonImportOnly})
		default:
			kept = append(kept, Exclusion{Path: e.Path, Reason: ReasonUncovered})
		}
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].Path < cands[j].Path })
	return cands, kept, promoted
}

// isLibraryCode asks p's OPTIONAL lang.LibraryCodeClassifier, when it
// implements one, whether path is library code — see that interface's own
// doc for the rule and why it exists. A plugin that does not implement it
// (every language except python today), or a path lang.Detect cannot
// resolve at all, is treated as library code: byte-identical to before
// this distinction existed, never a NEW exclusion for a language this
// package has no opinion about.
func isLibraryCode(path string, hasPath func(string) bool) bool {
	p, ok := lang.Detect(path)
	if !ok {
		return true
	}
	lc, ok := p.(lang.LibraryCodeClassifier)
	if !ok {
		return true
	}
	return lc.IsLibraryCode(path, hasPath)
}

// demoteAmbiguousPairings enforces, as a global property (not a per-plugin
// heuristic), that one test file grades exactly one source file. The
// per-plugin TestPaths ordering minimizes collisions but cannot rule them
// out — two sources can each legitimately resolve to the SAME test path
// (observed on real repos: flask's tests/test_views.py matched THREE
// distinct source files; tests/test_blueprints.py matched two). Signing a
// mutation-adequacy verdict for one file using a test suite that actually
// belongs to another is worse than not auditing the file at all, so this
// pass runs AFTER every source has independently resolved its pairing and
// removes every pairing that isn't safely unambiguous:
//
//   - Group the just-resolved candidates by TestPath.
//   - A group of size 1 is untouched — no collision.
//   - In a group of size >1, if exactly one member has a STRICTLY better
//     (lower) specificity rank than every other member, it is kept — a
//     sibling or full-directory-mirror match outranks a same-named test that
//     merely happens to also resolve via a less specific form for some other
//     file — and every other member is demoted.
//   - If the best rank is TIED across two or more members, ALL of them are
//     demoted. This does lose a correct pairing sometimes (a genuine
//     sibling match demoted because an unrelated file's own sibling match
//     collides implausibly) — that is the intended trade: an accounted
//     ReasonAmbiguousTest exclusion is honest; a coin-flip pairing that
//     happens to land on the right file some fraction of the time is not.
//
// rank[i] is the evidentiary specificity (lower = more specific) of the
// plugin's own lang.TestCandidate that resolved cands[i].TestPath — NOT its
// position in Enumerate's search loop, which would conflate "how specific is
// this match" with "how many earlier, more-specific-looking candidates
// happened to collapse onto the same string for this particular source". It
// is parallel to cands and produced by the same loop in Enumerate, never
// persisted on Candidate itself.
func demoteAmbiguousPairings(cands []Candidate, rank []int, excl []Exclusion, explicit []bool) ([]Candidate, []Exclusion) {
	groups := map[string][]int{} // TestPath -> indices into cands
	for i, c := range cands {
		// An EXPLICIT pairing is exempt. This guard exists to catch ACCIDENTAL
		// collisions — two sources a filename heuristic happened to point at
		// one test — because a wrong pairing plants mutants in one file and
		// grades them against another's tests. A tenant deliberately mapping
		// several sources onto one suite (express: every lib file to test/) is
		// a choice, not a collision, and demoting it would discard exactly the
		// pairings the operator supplied on purpose.
		if i < len(explicit) && explicit[i] {
			continue
		}
		groups[c.TestPath] = append(groups[c.TestPath], i)
	}

	demoted := map[int]bool{}
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		best := rank[idxs[0]]
		for _, i := range idxs[1:] {
			if rank[i] < best {
				best = rank[i]
			}
		}
		var atBest []int
		for _, i := range idxs {
			if rank[i] == best {
				atBest = append(atBest, i)
			}
		}
		if len(atBest) == 1 {
			// Exactly one strictly-best claimant: keep it, demote the rest.
			for _, i := range idxs {
				if i != atBest[0] {
					demoted[i] = true
				}
			}
			continue
		}
		// Tied for best (or, degenerately, every member shares one rank):
		// no safe winner — demote the whole group.
		for _, i := range idxs {
			demoted[i] = true
		}
	}
	if len(demoted) == 0 {
		return cands, excl
	}

	kept := cands[:0:0]
	for i, c := range cands {
		if demoted[i] {
			excl = append(excl, Exclusion{Path: c.Path, Reason: ReasonAmbiguousTest})
			continue
		}
		kept = append(kept, c)
	}
	return kept, excl
}

// isTestFile reports whether rel is itself a test file, detected by the
// naming markers the five language plugins use. The markers are the real
// check and do NOT depend on the shape of TestPaths at all — a parallel-tree
// test like tests/agents/test_artifact_store.py is caught by the "test_"
// prefix marker exactly like a sibling test_artifact_store.py would be, so
// widening TestPaths from one path to an ordered list changes nothing here.
//
// The fixed-point check below (does rel appear in ITS OWN TestPaths list) is
// a cheap belt-and-braces for a plugin that is someday idempotent on an
// already-test path — no current plugin is (`foo_test.go`'s own conventions
// produce `foo_test_test.go`, `test_test_foo.py`, etc, never `foo_test.go`
// itself), so this never fires today either.
func isTestFile(p lang.Plugin, rel string) bool {
	for _, tp := range p.TestPaths(rel) {
		if filepath.ToSlash(tp.Path) == rel {
			return true
		}
	}

	// Check against the basename only to avoid directory-component matches.
	base := filepath.Base(rel)

	// _test. suffix (Go: foo_test.go, Ruby minitest: foo_test.rb)
	if strings.Contains(base, "_test.") {
		return true
	}

	// test_ prefix (Python: test_foo.py, Ruby: test_foo.rb)
	if strings.HasPrefix(base, "test_") {
		return true
	}

	// _spec. suffix (Ruby RSpec: foo_spec.rb, JavaScript: foo_spec.js, TypeScript: foo_spec.ts)
	if strings.Contains(base, "_spec.") {
		return true
	}

	// .test. suffix (JavaScript: foo.test.js, TypeScript: foo.test.ts)
	if strings.Contains(base, ".test.") {
		return true
	}

	// .spec. suffix (JavaScript: foo.spec.js, TypeScript: foo.spec.ts)
	if strings.Contains(base, ".spec.") {
		return true
	}

	// spec_ prefix (Ruby: spec_foo.rb)
	if strings.HasPrefix(base, "spec_") {
		return true
	}

	return false
}
