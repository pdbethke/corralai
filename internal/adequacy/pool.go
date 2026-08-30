// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Disclosure is what the verdict and the report say about concurrency: how
// many private trees the run actually got, and — when that is one — WHY.
//
// It exists because the downgrade is silent otherwise. A pool that could not
// copy the checkout still runs, correctly, at concurrency 1; without the note
// the operator sees a slow audit and no reason for it, and a report that
// claims nothing about the substrate it ran on.
type Disclosure struct {
	Trees int    // 1 when downgraded
	Note  string // "" when Trees > 1; otherwise WHY the downgrade happened
	// Shared names the dependency directories (in symlinkedDepDirs order)
	// that exist at the checkout and were SYMLINKED into every tree rather
	// than copied. It is populated whenever such links were made — including
	// on a healthy Trees > 1 pool — and is nil for a pool of one, which links
	// nothing because it runs on the checkout itself.
	//
	// It is disclosure, not decoration: these directories are the one thing
	// the trees do NOT hold privately. A suite that writes into its own
	// .venv/node_modules/.tox during a run is writing through a shared link
	// into the operator's real checkout, and that is a channel between trees
	// the isolation argument otherwise rules out. The report has to be able
	// to say which ones they were.
	Shared []string
	// CopyDuration is how long copying the checkout into N private trees
	// took, and ProbeDuration how long the concurrency probe's 2N suite
	// invocations took. Together they are the price of parallelism paid
	// BEFORE a single mutant is scored, and both were unmeasured: a file
	// whose audit spent minutes on setup reported only its dev pass, so the
	// minutes had nowhere to appear.
	//
	// Both are ZERO for a pool of one, which copies nothing and probes
	// nothing — and zero here is recorded as SQL NULL downstream, never as a
	// setup that was free. On a DOWNGRADE they are non-zero and honest: the
	// copies were made and the probe did run; the answer was simply that the
	// trees could not be used.
	CopyDuration  time.Duration
	ProbeDuration time.Duration
}

// maxUniverseBytes bounds the checkout a pool is willing to copy N times.
// Above it the copies are the dominant cost of the audit rather than a
// rounding error against the suite runtime the parallelism exists to overlap,
// so the pool downgrades to the checkout itself and says so.
const maxUniverseBytes = 2 << 30 // 2 GiB

// symlinkedDepDirs are dependency trees that are ignored by git (so they are
// not in the universe to copy) but MUST still be reachable from every tree:
// a suite that cannot import its own dependencies fails for a reason that has
// nothing to do with the mutant. They are symlinked, not copied — they are
// large, they are build products, and no mutant is ever written into them.
//
// Every entry has to satisfy that last clause: the RUN must not write into
// it. That is why .tox is NOT here. tox creates, updates and reuses its
// environments inside .tox as part of running the suite, so a shared .tox
// would be N trees writing to one directory at once (cross-tree interference
// on the very isolation this pool exists to provide) AND a write through the
// link into the operator's real checkout. A tox suite in a tree simply
// rebuilds its own .tox, which is slower and correct.
var symlinkedDepDirs = []string{"node_modules", "vendor", ".venv", "venv", ".bundle"}

// WorkspacePool is a Jail backed by N private copies of one checkout. Each
// RunTest borrows a free tree, applies the files there, runs, restores, and
// returns it — so adequacy.Score's WithConcurrency becomes safe on the
// workspace substrate, which a single WorkspaceRunner can never make it (it
// mutates ONE tree in place with no mutex; two concurrent runs interleave
// their mutants and every verdict after that is fiction).
//
// With n == 1 it IS a WorkspaceRunner on the checkout: no copy, nothing to
// clean up, and Close is a no-op on the operator's own tree.
type WorkspacePool struct {
	// runners holds one runner per tree, in tree order, for treeRoots,
	// Verify and Enumerate. Ownership for RUNS is handed out through free.
	runners []*WorkspaceRunner
	// free is the borrow queue: a receive takes a tree out of circulation for
	// the duration of one run, a send puts it back. A channel rather than a
	// mutex+slice because a borrower must BLOCK when every tree is busy,
	// which is exactly how Score's concurrency limiter and this pool's size
	// stay consistent even when the caller sets them differently.
	free chan *WorkspaceRunner
	// temps are the directories Close removes. Empty for a pool of one: root
	// belongs to the operator and is NEVER removed.
	temps []string

	// root, timeout and opts are the construction inputs, retained so a
	// caller that has to DOWNGRADE mid-flight (a copy went bad, the operator
	// capped concurrency) can rebuild a one-tree pool on the same checkout
	// with the same configuration instead of threading all three through its
	// own call stack alongside the pool.
	root    string
	timeout time.Duration
	opts    []WorkspaceOption

	// copyDuration is what building this pool's trees cost, retained for the
	// same reason `shared` is: Probe returns a FRESH Disclosure, and a
	// caller that records only the probe's answer must not lose the half of
	// the cost that was paid before the probe ran.
	copyDuration time.Duration

	// shared is the Disclosure.Shared this pool was built with, retained so
	// Probe can restate it on the disclosure it returns: the dep dirs stay
	// shared for as long as these trees do, and a caller that records only
	// the probe's answer must not lose that half of the disclosure.
	shared []string
}

// The pool must be substitutable for a WorkspaceRunner everywhere one is used
// today, so every interface a caller may assert is asserted here at COMPILE
// time — a missing method would otherwise degrade silently at run time (a
// failed type assertion in advpool costs the test-writer its compiler output;
// a missing Enumerate costs reposcan its coverage pre-flight) rather than
// failing the build.
var (
	_ Jail       = (*WorkspacePool)(nil)
	_ Enumerator = (*WorkspacePool)(nil)
	// advpool's verboseJail is unexported, so the method set is asserted
	// against a local interface literal of the identical shape.
	_ interface {
		RunTestVerbose(ctx context.Context, files map[string]string, cmd []string) (bool, string, error)
	} = (*WorkspacePool)(nil)
)

// NewWorkspacePool builds a pool of n private trees copied from the checkout
// at root, each a WorkspaceRunner bounded by timeout and configured with
// opts.
//
// The construction is best-effort by design: every reason it cannot give the
// caller n trees produces a working pool of ONE (the checkout itself) plus a
// Disclosure saying why, never an error — an audit that runs slowly and says
// so beats an audit that refuses to run. err is reserved for a failure that
// leaves nothing usable at all.
//
//   - n <= 1: the checkout itself, no copy, Disclosure{Trees: 1}.
//   - root is not inside a git work tree (or git is missing): there is no
//     authority on what the checkout CONTAINS — copying a raw walk would
//     drag in build output, caches and dep trees — so Trees: 1, note
//     "not a git work tree".
//   - the universe exceeds 2 GiB: Trees: 1, note "checkout over 2 GiB".
//
// WithTreeEnv is lifted out of opts and honoured here, per tree; every other
// option is passed through to each tree's NewWorkspaceRunner unchanged.
func NewWorkspacePool(ctx context.Context, root string, n int, timeout time.Duration, opts ...WorkspaceOption) (*WorkspacePool, Disclosure, error) {
	// Absolute from here on. The dep-dir symlinks below carry root as their
	// TARGET, and `--repo .` hands us "." — a link whose target is the relative
	// ".venv" resolves inside the tree, to itself, and every command run
	// through it dies with ELOOP. The first real run found this; the probe
	// downgraded correctly and the disclosure said why, which is the only
	// reason it was a slow run and not a wrong one.
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	// The probe exists to read the two env-shaped options back out: they are
	// opaque funcs, so there is no way to inspect the list without applying
	// it. Applying it to a throwaway runner is that inspection.
	probe := &WorkspaceRunner{}
	for _, o := range opts {
		o(probe)
	}
	treeEnv, plugEnv := probe.treeEnv, probe.perRunEnv

	single := func(note string) (*WorkspacePool, Disclosure, error) {
		// A pool of one links nothing — it IS the checkout — so Shared stays
		// nil no matter what dep dirs are present.
		//
		// And it carries NO TREE ENV, which is the whole point of passing nil
		// here rather than treeEnv. WithTreeEnv describes what a COPY needs in
		// order to behave like the checkout: its own root on PYTHONPATH so it
		// cannot import the original through an editable install's .pth, and
		// its own SHARE of the box (GOMAXPROCS, -p) because it is one of N.
		// Applied to the operator's real checkout, both are wrong and both
		// are harmful — the run would be pinned to cores/N with no other tree
		// to yield to (SLOWER than never having tried to parallelise), and a
		// PYTHONPATH would be injected into a suite that never asked for one.
		//
		// This is the path every downgrade lands on, including Probe's: the
		// contract is that a pool of one on root is today's WorkspaceRunner,
		// byte for byte, and an env the caller only meant for copies would
		// break that silently.
		p := newPool(root, []string{root}, nil, timeout, nil, plugEnv, opts)
		return p, Disclosure{Trees: 1, Note: note}, nil
	}

	if n <= 1 {
		return single("")
	}
	if !insideGitWorkTree(root) {
		return single("not a git work tree")
	}
	universe, size, err := gitUniverse(root)
	if err != nil {
		return single("git could not list the checkout: " + err.Error())
	}
	if size > maxUniverseBytes {
		return single("checkout over 2 GiB")
	}

	// THE COPY. Measured around the whole loop, not per tree: N trees is one
	// decision and one cost, and the audit paid all of it before it could
	// score anything.
	copyStart := time.Now()
	var temps []string
	var linked []string
	cleanup := func() {
		for _, d := range temps {
			_ = os.RemoveAll(d)
		}
	}
	for i := 0; i < n; i++ {
		if cerr := ctx.Err(); cerr != nil {
			cleanup()
			return single("cancelled while copying the checkout")
		}
		tree, terr := os.MkdirTemp("", "corral-tree-*")
		if terr != nil {
			cleanup()
			return single("could not create a tree: " + terr.Error())
		}
		temps = append(temps, tree)
		shared, cerr := copyTree(root, tree, universe)
		if cerr != nil {
			cleanup()
			return single("could not copy the checkout: " + cerr.Error())
		}
		linked = shared // identical for every tree: same root, same probe
	}
	copied := time.Since(copyStart)
	p := newPool(root, temps, temps, timeout, treeEnv, plugEnv, opts)
	p.shared = linked
	p.copyDuration = copied
	return p, Disclosure{Trees: n, Shared: linked, CopyDuration: copied}, nil
}

// newPool wires one runner per tree. temps is the subset of trees Close may
// remove (nil when the single "tree" is the operator's own checkout).
func newPool(root string, trees, temps []string, timeout time.Duration, treeEnv func(string) []string, plugEnv func() ([]string, func()), opts []WorkspaceOption) *WorkspacePool {
	p := &WorkspacePool{free: make(chan *WorkspaceRunner, len(trees)), temps: temps, root: root, timeout: timeout, opts: opts}
	for _, tree := range trees {
		// The composed per-run env is appended LAST so it wins over any
		// WithPerRunEnv already in opts — that option's func is not dropped,
		// it is called from inside this one. Tree env first, plugin env
		// second, so a plugin can still override a tree-derived value.
		treeOpts := append(append([]WorkspaceOption{}, opts...), WithPerRunEnv(composeRunEnv(tree, treeEnv, plugEnv)))
		r := NewWorkspaceRunner(tree, timeout, treeOpts...)
		p.runners = append(p.runners, r)
		p.free <- r
	}
	return p
}

// composeRunEnv returns the per-run env source for one tree: the caller's
// tree rules evaluated for THIS tree, then the language plugin's own per-run
// env, whose cleanup is preserved and run exactly as it would be without the
// pool. nil when neither is set, so a pool adds no env of its own.
func composeRunEnv(tree string, treeEnv func(string) []string, plugEnv func() ([]string, func())) func() ([]string, func()) {
	if treeEnv == nil && plugEnv == nil {
		return nil
	}
	return func() ([]string, func()) {
		var env []string
		if treeEnv != nil {
			env = append(env, treeEnv(tree)...)
		}
		cleanup := func() {}
		if plugEnv != nil {
			// Called FRESH per run, exactly as WithPerRunEnv's contract
			// requires — the pool never hoists it to construction time.
			extra, c := plugEnv()
			env = append(env, extra...)
			if c != nil {
				cleanup = c
			}
		}
		return env, cleanup
	}
}

// insideGitWorkTree reports whether root is inside a git work tree, the same
// probe internal/reposcan uses: git is the only authority on what a checkout
// contains, and a non-zero exit ("not a git repository") is an ANSWER, not a
// failure.
func insideGitWorkTree(root string) bool {
	git, lerr := exec.LookPath("git")
	if lerr != nil {
		return false
	}
	probe := exec.Command(git, "-C", root, "rev-parse", "--is-inside-work-tree") // #nosec G204 -- git via LookPath; root is the operator's own checkout; literal args
	// A non-zero exit ("not a git repository") and a failure to run git at
	// all are the same answer here: there is no authority to consult, so the
	// pool downgrades rather than guessing at the checkout's contents.
	out, perr := probe.Output()
	if perr != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// gitUniverse is what a tree must contain to be a usable copy of the
// checkout: every tracked file plus every untracked-but-not-ignored one —
// git's own answer, so nested .gitignore files, core.excludesFile and
// negations are all honoured without this package reimplementing any of it.
// Ignored paths (build output, caches, dep trees) are deliberately absent;
// the dep dirs a suite actually needs come back as symlinks in copyTree.
//
// Returns the repo-relative paths in git's order and their total size in
// bytes. A path that has vanished between the listing and the stat is skipped
// rather than fatal: an untracked file is exactly the kind of thing an editor
// or a test run deletes underneath us.
func gitUniverse(root string) (paths []string, size int64, err error) {
	git, lerr := exec.LookPath("git")
	if lerr != nil {
		return nil, 0, lerr
	}
	ls := exec.Command(git, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard") // #nosec G204 -- git via LookPath; root is the operator's own checkout; literal args
	out, lserr := ls.Output()
	if lserr != nil {
		return nil, 0, fmt.Errorf("git ls-files in %s: %w", root, lserr)
	}
	seen := make(map[string]bool)
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		fi, serr := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if serr != nil || !fi.Mode().IsRegular() {
			continue // gone, or a symlink/submodule/device: not ours to copy
		}
		paths = append(paths, rel)
		size += fi.Size()
	}
	return paths, size, nil
}

// copyTree materialises one private tree: the dep-dir symlinks first, then
// every universe file copied in with its mode preserved. It returns the
// dep dirs it actually linked, which is what Disclosure.Shared reports.
//
// Symlinks go first on purpose. A path under a symlinked dep dir must never
// be written through it — that would write into the ORIGINAL checkout, which
// is the one thing a private tree exists to prevent — so anything in the
// universe that lands under one of them is skipped, and skipping requires
// knowing which links exist.
func copyTree(root, tree string, universe []string) (shared []string, err error) {
	linked := make(map[string]bool)
	for _, d := range symlinkedDepDirs {
		if _, lerr := os.Lstat(filepath.Join(root, d)); lerr != nil {
			continue
		}
		if serr := os.Symlink(filepath.Join(root, d), filepath.Join(tree, d)); serr != nil {
			return nil, fmt.Errorf("adequacy: linking %s into %s: %w", d, tree, serr)
		}
		linked[d] = true
		shared = append(shared, d)
	}
	for _, rel := range universe {
		if top, _, ok := strings.Cut(filepath.ToSlash(rel), "/"); ok && linked[top] {
			continue
		}
		if cerr := copyFile(filepath.Join(root, filepath.FromSlash(rel)), filepath.Join(tree, filepath.FromSlash(rel))); cerr != nil {
			return nil, cerr
		}
	}
	return shared, nil
}

// copyFile copies src to dst, creating dst's parents and giving dst src's
// permission bits — an executable in the checkout (a test script, a hook)
// must still be executable in the copy.
func copyFile(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("adequacy: stat %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("adequacy: creating %s: %w", filepath.Dir(dst), err)
	}
	in, err := os.Open(src) // #nosec G304 -- a path git itself listed inside the operator's checkout
	if err != nil {
		return fmt.Errorf("adequacy: opening %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst) // #nosec G304 -- inside a temp tree this call created
	if err != nil {
		return fmt.Errorf("adequacy: creating %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("adequacy: copying %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("adequacy: closing %s: %w", dst, err)
	}
	if err := os.Chmod(dst, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("adequacy: chmod %s: %w", dst, err)
	}
	return nil
}

// borrow takes a free tree out of circulation, blocking until one is
// available or ctx is done. The returned func puts it back and MUST be
// deferred: a tree that is never returned is one worker's worth of
// concurrency gone for the rest of the audit.
func (p *WorkspacePool) borrow(ctx context.Context) (*WorkspaceRunner, func(), error) {
	select {
	case r := <-p.free:
		return r, func() { p.free <- r }, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// RunTest borrows a tree, runs there, and returns it — the Jail contract,
// now safe to call concurrently up to Trees() at a time.
func (p *WorkspacePool) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	r, release, err := p.borrow(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return r.RunTest(ctx, files, testCmd)
}

// RunTestVerbose is RunTest that also returns the command's combined output,
// the optional interface advpool's compile-verify path type-asserts for. The
// pool implements it so substituting a pool for a WorkspaceRunner never
// silently costs the test-writer its compiler output.
func (p *WorkspacePool) RunTestVerbose(ctx context.Context, files map[string]string, testCmd []string) (bool, string, error) {
	r, release, err := p.borrow(ctx)
	if err != nil {
		return false, "", err
	}
	defer release()
	return r.RunTestVerbose(ctx, files, testCmd)
}

// Enumerate borrows a tree exactly as RunTest does, then runs there.
//
// Borrowing is NOT optional even though enumeration is nominally a pre-flight.
// Enumerate goes through the identical applyFiles/restore ledger a mutant run
// does, so an enumeration sharing a tree with an in-flight RunTest interleaves
// two ledgers on one checkout — each restoring what it believes the original
// bytes were — which is precisely the false-kill this whole type exists to
// prevent. "Nothing else is running right now" is a claim about the caller,
// not a property of this type, and it is not one Enumerate can check.
func (p *WorkspacePool) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	r, release, err := p.borrow(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return r.Enumerate(ctx, files, cmd)
}

// Verify pre-flights every tree, so a copy that failed to materialise is
// caught before the first job wastes time discovering it.
func (p *WorkspacePool) Verify() error {
	for _, r := range p.runners {
		if err := r.Verify(); err != nil {
			return err
		}
	}
	return nil
}

// Trees is how many runs the pool can serve at once — the number the caller
// should cap adequacy.Score's WithConcurrency at.
func (p *WorkspacePool) Trees() int { return len(p.runners) }

// treeRoots is the pool's trees in order, for tests and for a caller that
// needs to name them (a tree-env rule). Not exported: nothing outside this
// package has any business writing into a tree directly.
func (p *WorkspacePool) treeRoots() []string {
	roots := make([]string, 0, len(p.runners))
	for _, r := range p.runners {
		roots = append(roots, r.root)
	}
	return roots
}

// Close removes the copies. It NEVER removes root: a pool of one has no
// temp trees at all, and its temps slice is empty by construction. Safe to
// call more than once.
func (p *WorkspacePool) Close() {
	for _, d := range p.temps {
		_ = os.RemoveAll(d)
	}
	p.temps = nil
}

// probeOutputCap bounds how much of a failing tree's output the note carries.
// The note travels into the verdict and the report, where it has to stay
// readable next to the numbers; a suite that fails under concurrency usually
// says so in its first few lines, and a full pytest dump would bury it.
const probeOutputCap = 2 << 10 // 2 KiB

// Probe runs the unmutated baseline AND the canary in every tree at once.
// It returns the pool to score with: this one, or a 1-tree pool on the
// checkout with the reason — never an error for a suite that merely is not
// concurrency-safe. A pool of one returns itself untouched.
//
// The two questions it asks are the two ways N private trees can be wrong:
//
//   - The baseline must PASS in every tree simultaneously. It fails when the
//     suite is not concurrency-safe (a fixed port, a shared lock, one
//     scratch database) — and equally when the copies are missing something
//     the suite needs that git could not report: a .git directory the suite
//     shells out to, a tracked-empty directory it writes into. Either way the
//     baseline output says what broke, and THAT output is the disclosure.
//   - The canary must FAIL in every tree. A canary that passes means the tree
//     ran against code that is not the tree's — the editable-install trap,
//     where a .pth or an installed package points back at the ORIGINAL
//     checkout. Left alone it survives every mutant and reports a kill rate
//     of zero, which is worse than not running.
//
// Probe runs the WHOLE testCmd it is handed — the file's baseline command —
// which may be a superset of the selection-narrowed per-mutant command the
// scorer later runs. That is conservative in the safe direction: more tests
// contend for more shared resources, so the error is a false DOWNGRADE (a
// slower audit, disclosed), never a false pass that scores mutants in trees
// the suite cannot actually share.
//
// Each tree is exercised on its OWN runner rather than through the borrow
// queue, because running all N at once is the entire measurement — borrowing
// would happily serve the same tree twice. That makes the pool's exclusive
// idleness a PRECONDITION: no other run may be in flight against p while
// Probe is running.
func (p *WorkspacePool) Probe(ctx context.Context, base map[string]string, codePath, compliantCode string, testCmd []string) (*WorkspacePool, Disclosure) {
	n := p.Trees()
	if n <= 1 {
		// Nothing was copied and nothing is about to be probed: this pool IS
		// the checkout. Both durations stay zero, which every reader records
		// as "did not happen".
		return p, Disclosure{Trees: 1}
	}

	// THE PROBE's own clock, started before the first of its two rounds and
	// read on every path out — including the downgrades, which spent the
	// time and then discovered the trees were unusable.
	probeStart := time.Now()

	downgrade := func(note string) (*WorkspacePool, Disclosure) {
		// The copies are dead the moment the answer is "one tree": nothing
		// will run in them again, and they are the operator's disk.
		one, _, err := NewWorkspacePool(ctx, p.root, 1, p.timeout, p.opts...)
		if err != nil || one == nil {
			// A pool of one has nothing to construct and nothing to fail at,
			// so this is unreachable in practice; if it ever is reached, the
			// honest answer is to keep scoring on the pool we have and say
			// only what we know.
			return p, Disclosure{Trees: n, Note: note, CopyDuration: p.copyDuration, ProbeDuration: time.Since(probeStart)}
		}
		spent := time.Since(probeStart)
		copied := p.copyDuration
		p.Close()
		// Shared is deliberately nil: a pool of one runs on the checkout
		// itself and links nothing, so the copies' shared dep dirs are no
		// longer true of the pool being returned.
		// The copies and the probe were PAID FOR even though the answer was
		// "one tree": a downgraded file's audit really did cost this, and
		// reporting nothing would make the slowest possible outcome — copy,
		// probe, then serialize anyway — look like the cheapest.
		return one, Disclosure{Trees: 1, Note: note, CopyDuration: copied, ProbeDuration: spent}
	}

	baseOK, baseOut := p.runEverywhere(ctx, base, codePath, compliantCode, testCmd)
	for i, ok := range baseOK {
		if !ok {
			return downgrade(fmt.Sprintf("suite is not concurrency-safe: baseline failed under %d — %s", n, capOutput(baseOut[i])))
		}
	}
	canaryOK, _ := p.runEverywhere(ctx, base, codePath, CanaryCode, testCmd)
	for _, ok := range canaryOK {
		if ok {
			return downgrade(fmt.Sprintf("a tree imports the original checkout (canary passed under %d)", n))
		}
	}
	return p, Disclosure{Trees: n, Shared: p.shared, CopyDuration: p.copyDuration, ProbeDuration: time.Since(probeStart)}
}

// runEverywhere runs one variant of codePath in EVERY tree simultaneously,
// one goroutine per tree pinned to that tree's own runner, and returns the
// pass flag and output per tree in tree order. An error from the runner is a
// failure with the error as its output: the probe's whole job is to answer
// "did this work in all N", and a tree that could not even run did not.
func (p *WorkspacePool) runEverywhere(ctx context.Context, base map[string]string, codePath, code string, testCmd []string) ([]bool, []string) {
	oks := make([]bool, len(p.runners))
	outs := make([]string, len(p.runners))
	files := make(map[string]string, len(base)+1)
	for k, v := range base {
		files[k] = v
	}
	files[codePath] = code

	var wg sync.WaitGroup
	for i, r := range p.runners {
		wg.Add(1)
		go func(i int, r *WorkspaceRunner) {
			defer wg.Done()
			// Each tree gets its own copy of the map: applyFiles must never
			// see one shared map mutated from N goroutines.
			mine := make(map[string]string, len(files))
			for k, v := range files {
				mine[k] = v
			}
			ok, out, err := r.RunTestVerbose(ctx, mine, testCmd)
			if err != nil {
				oks[i], outs[i] = false, err.Error()
				return
			}
			oks[i], outs[i] = ok, out
		}(i, r)
	}
	wg.Wait()
	return oks, outs
}

// capOutput trims a tree's output to what a note can carry, collapsing the
// whitespace a shell leaves around it so the reason reads as one sentence.
func capOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > probeOutputCap {
		s = s[:probeOutputCap] + "… (truncated)"
	}
	return s
}
