// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
)

func init() { Register(rubyPlugin{}) }

type rubyPlugin struct{}

func (rubyPlugin) Name() string                { return "ruby" }
func (rubyPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".rb" }

// Scaffold is empty: a Ruby test require_relative's its sibling module from the
// workspace root.
func (rubyPlugin) Scaffold() map[string]string { return map[string]string{} }

// TestCmd dispatches by test-file CONTENT at run time: RSpec files (require
// rspec / RSpec.) go to the rspec runner, everything else runs with plain
// `ruby` (minitest self-runs via require 'minitest/autorun'). The pool renames
// the dev test to a neutral TestPath, so the filename carries no framework
// signal — content does.
//
// TestCmd genuinely needs shell logic — it discovers the test file and chooses
// between rspec and plain ruby — so it invokes a shell EXPLICITLY rather than
// relying on a caller to shell-join it.
//
// It previously returned the script as a single argv element. That works only
// where something joins argv and runs it under `sh -c` (the jail substrate);
// the workspace substrate execs argv directly, so argv[0] became the literal
// program name `t="$(ls` and the run died with "executable file not found in
// $PATH". That is how the first real rubocop audit failed — and CompileCheck,
// immediately below, already documented this exact bug class and how to avoid
// it. {"sh", "-c", script} is correct on BOTH substrates: the workspace execs
// sh, and the jail's shell-join wraps it in another sh -c, which nests
// harmlessly.
func (rubyPlugin) TestCmd() []string {
	return []string{"sh", "-c",
		`t="$(ls *_test.rb *_spec.rb test_*.rb spec_*.rb 2>/dev/null | head -n1)"; ` +
			`[ -z "$t" ] && { echo "no ruby test file"; exit 1; }; ` +
			`if grep -Eq "require ['\"](rspec|spec_helper)|RSpec[.:]" "$t"; then exec rspec "$t"; else exec ruby "$t"; fi`,
	}
}

// CompileCheck syntax-checks BOTH files with `ruby -c` (offline). `ruby -c`
// only ever reports on a SINGLE file per invocation (`ruby -c a -c b` is not
// valid), so this returns a TWO-command sequence — codePath then testPath,
// run in order, stopping at the first failure — rather than trying to splice
// a `&&` into one argv element. A `&&`-joined single element only ever
// worked because the jail substrate shell-joins argv and runs it under
// `sh -c`; the workspace substrate execs argv directly with no shell, where
// `&&` would just be handed to `ruby -c` as a literal (nonexistent) filename
// — see lang.Plugin.CompileCheck's doc comment and python.go's
// pyCachePrefixEnv comment for the identical class of bug this avoids.
//
// `ruby -c` is SYNTAX only. A mutant that fails at LOAD — `LIMIT =
// UNDEFINED_CONSTANT` at the top level, `class Calc < DoesNotExist` — parses
// fine and then makes every test that requires the file fail, so it read as
// KILLED and inflated the kill rate with breakage any suite catches. Go's
// gate (a real compile) and Python's (ruff F821) treat the same shape as
// INVALID; the third command here loads the file the way a test would, in
// the same sandbox, and is probed on compliant code first like every gate
// command (adequacy.Score disables a gate that rejects the unmutated file).
func (rubyPlugin) CompileCheck(codePath, testPath string) [][]string {
	return [][]string{
		{"ruby", "-c", codePath},
		{"ruby", "-c", testPath},
		{"ruby", "-e", "require File.expand_path(ARGV[0])", "--", codePath},
	}
}

// TestPaths covers Ruby's three common test-file conventions, framework-
// neutral (content, not the name, selects minitest vs rspec at run time),
// most specific first:
//
//  1. sibling foo_test.rb           — same directory as the source.
//  2. test/<subpath>/foo_test.rb    — the classic lib/ vs test/ layout,
//     where <subpath> is dir with its leading component (conventionally
//     `lib`) replaced by `test` rather than nested under it.
//  3. spec/<subpath>/foo_spec.rb    — the RSpec equivalent of (2).
//  4. test/<subpath>/test_foo.rb    — the PREFIX form, minitest's own house
//     style, at a strictly lower specificity rank than (1)-(3). Measured on
//     minitest/minitest: forms (1)-(3) pair 0 of 24 lib files, form (4) pairs
//  4. A repo using this layout was previously invisible to the scanner
//     entirely, reported as `no-paired-test`.
//
// Unlike Python's list this does not also generate a full-directory-mirror
// form: Ruby's lib/-vs-test/ (or lib/-vs-spec/) split is a single well-known
// convention, not a family of layouts, so there is no comparably plausible
// second parallel-tree shape to hedge against.
func (rubyPlugin) TestPaths(codePath string) []TestCandidate {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)

	out := []TestCandidate{
		{Path: joinDir(dir, base+"_test.rb"), Rank: 0},
		{Path: filepath.Join("test", sub, base+"_test.rb"), Rank: 1},
		{Path: filepath.Join("spec", sub, base+"_spec.rb"), Rank: 1},
		// The PREFIX form, minitest's own house style. Rank 2 — strictly less
		// specific than the suffix forms above, because `test_foo.rb` is also
		// what a file *named* `test_foo` would pair to under form (1), so a
		// collision here should lose to a suffix match rather than race it.
		{Path: filepath.Join("test", sub, "test_"+base+".rb"), Rank: 2},
	}
	return dedupeCandidates(out)
}

// TestRoots names Ruby's own additional conventional test roots (beyond
// reposcan's generic "tests" default): the classic lib/-vs-test/ split, and
// its RSpec equivalent lib/-vs-spec/.
func (rubyPlugin) TestRoots() []string { return []string{"test", "spec"} }

// Preflight requires only `ruby` (minitest is bundled) — or, when the
// operator named an explicit test command (e.g. `bundle exec rspec`, or an
// interpreter under a version manager's shim not on PATH under "ruby"),
// THAT command's own binary; see preflightBin and Plugin.Preflight's doc
// comment. It deliberately does NOT require the rspec gem: only a run whose
// DEV suite is RSpec needs it, and a missing gem then fails the jail
// command — fail-closed, never a false pass.
func (rubyPlugin) Preflight(testCmd []string) error {
	return toolOnPath(preflightBin(testCmd, "ruby"))
}

func (rubyPlugin) PromptLang() string { return "Ruby" }

func (rubyPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Ruby test that verifies the code SATISFIES the goal.
- MATCH THE PROJECT'S HARNESS, which the developer's own test (shown to you) uses: if it is RSpec (` + "`RSpec.describe`" + `, a ` + "`_spec.rb`" + ` file), write an RSpec spec that ` + "`require`" + `s the project's spec helper the same way; otherwise write minitest — start with ` + "`require 'minitest/autorun'`" + `, ` + "`require_relative`" + ` the target module by its file's base name (e.g. ` + "`require_relative 'pricing'`" + ` for pricing.rb), and define a Minitest::Test subclass.
- It MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library plus the project's own test framework only (no other gems). Deterministic, no network.
Return ONLY the raw Ruby test file content — no prose, no markdown fences.`
}

func (rubyPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same public method signatures (drop-in Ruby that loads) and must genuinely fail the goal — vary HOW it fails. No no-ops, no syntax errors, no tests.
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

// ImportPath is always ok=false: `require_relative 'pricing'` resolves
// relative to the FILE ISSUING THE REQUIRE — i.e. the authored test's own
// directory — and roles.go always places that test in the SAME directory
// as the code under test, so the base-name require already works no matter
// how deep the source sits in the tree. Nothing here to correct (see
// lang.Plugin.ImportPath's doc comment for the general rule this follows).
func (rubyPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — Ruby's require_relative needs
// no per-task correction.
func (rubyPlugin) ImportNote(string, bool) string { return "" }

func (rubyPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }

func (rubyPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (rubyPlugin) ParseTestList(string) []string { return nil }

// WorkspaceRunEnv is a no-op: MRI has no persistent, mtime/size-keyed
// bytecode cache next to a .rb source file by default (unlike CPython's
// __pycache__) — nothing here is exposed to the workspace substrate's
// same-second, same-length staleness hole. See lang.Plugin.WorkspaceRunEnv's
// doc comment.
func (rubyPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// FailFastArgs is rspec's `--fail-fast`. Minitest is deliberately ABSENT:
// its own --fail-fast arrived in minitest 5.22, and an older minitest treats
// an unknown option as an error — which the scorer would read as a kill for
// every mutant. rspec is the one Ruby runner whose flag corral is sure of.
// See lang.FailFaster.
func (rubyPlugin) FailFastArgs(testCmd []string) ([]string, bool) {
	if len(testCmd) == 0 || cmdIsShellWrapped(testCmd) {
		return nil, false
	}
	if cmdHasWord(testCmd, "rspec") {
		return []string{"--fail-fast"}, true
	}
	return nil, false
}

// COVERAGE FOR RUBY, WITH NO GEM TO INSTALL.
//
// Ruby ships `coverage` in the standard library, so this needs nothing in the
// audited project's Gemfile — which matters, because the pre-flight runs
// against a STRANGER'S repository and must not ask it to add a dependency
// before corral will look at it. SimpleCov is the usual answer and is the
// wrong one here: it wants a `require` at the top of spec_helper.rb, i.e. an
// edit to the tree under audit.
//
// The trick is RUBYOPT. Ruby loads `-r` requires BEFORE the main script, which
// is the only window in which Coverage.start can see the application files
// load — start it any later and the files already parsed are invisible. rspec,
// minitest, rake and a bare `ruby` invocation are all ruby processes, so one
// mechanism covers every way a Ruby suite is actually launched.
// rubyCoveragePreload is the script RUBYOPT loads before the suite. It writes
// to a FILE rather than stdout, and the shell cats that file afterwards: a
// suite's own output shares stdout, and interleaving a report into it would
// make the report unparseable exactly when the suite is noisiest.
const rubyCoveragePreload = `require 'coverage'
require 'pathname'
# METHOD coverage, not just line coverage, and the difference is not academic.
# A file that is merely REQUIRED runs its own class/def declaration lines, so
# line coverage reports it as executed even when the suite never calls a single
# thing in it. Measured directly on a fixture: a class that is required and
# never used reports lines_hit 2/3 — indistinguishable from a file under test —
# while method coverage reports methods_called 0/1, which is the truth. Since
# this map decides which files are AUDITABLE, line coverage would nominate
# every file anything happened to import.
begin
  Coverage.start(lines: true, methods: true)
rescue ArgumentError, TypeError
  # Rubies before 2.6 have no method coverage. Fall back rather than refuse:
  # a coarser measurement is still worth more than none, and ParseCoverage's
  # tri-state means a false positive here reads as "auditable", never as a
  # gap that was proven absent.
  Coverage.start
end
# WHOEVER CALLS Coverage.result FIRST, WE SEE THE DATA. Our at_exit is
# registered at startup (via RUBYOPT -r), so it runs LAST — after SimpleCov's
# own at_exit, which calls Coverage.result, which STOPS and CLEARS coverage.
# On every project with SimpleCov in its helper our hook then found
# "coverage measurement is not enabled" and the pre-flight was dead, with a
# diagnosis that blamed the suite for importing nothing. Wrapping result()
# captures the snapshot on the first call, whoever makes it, and hands the
# caller exactly what it asked for; our own hook writes only if nothing has.
module CorralCov
  @written = false
  def self.written?; @written; end
  def self.mark!; @written = true; end
end
Coverage.singleton_class.prepend(Module.new do
  def result(*args, **kw)
    snap = begin
      peek_result
    rescue StandardError
      nil
    end
    CorralCov.write(snap) if snap && !CorralCov.written?
    args.empty? && kw.empty? ? super() : super
  end
end)
def CorralCov.write(res)
  CorralCov.mark!
  begin
    # ONE FILE PER PROCESS, not one shared file.
    #
    # A single shared path opened 'w' is truncated by every process that
    # inherits it, and the PARENT exits LAST — so for any suite that runs its
    # tests in a subprocess (a Rakefile that shells out, phpunit's paratest
    # workers), the child's real report was overwritten by the parent's nearly
    # empty one. That does not merely lose data: a file the child executed came
    # back as a 0, i.e. corral reported a covered file under "measured and
    # NEVER executed by the suite", which is the only actionable list it
    # prints. The JavaScript path never had this bug because V8 writes one JSON
    # per process into a directory; this is that design, applied here.
    dir = ENV['CORRAL_COV_DIR']
    dest = File.join(dir, "#{Process.pid}-#{object_id}.txt")
    File.open(dest, 'w') do |out|
      res.each do |path, data|
        if data.is_a?(Hash)
          methods = data[:methods] || {}
          lines = data[:lines] || []
          # A file with no methods at all (a script, a constants file) can
          # only be judged by its lines. A file WITH methods is judged by
          # whether any of them ran.
          hit = if methods.empty?
                  lines.any? { |c| !c.nil? && c > 0 } ? 1 : 0
                else
                  methods.values.any? { |c| !c.nil? && c > 0 } ? 1 : 0
                end
          measurable = !methods.empty? || lines.any? { |c| !c.nil? }
        else
          lines = data
          next unless lines.respond_to?(:any?)
          measurable = lines.any? { |c| !c.nil? }
          hit = lines.any? { |c| !c.nil? && c > 0 } ? 1 : 0
        end
        # nil marks a line that cannot be executed (blank, comment, an 'end').
        # A file with nothing measurable is SKIPPED, not reported as
        # never-executed: absent means "not measured", which is the honest
        # answer for a file that had no statement to skip.
        next unless measurable
        # RELATIVE TO THE WORKING DIRECTORY, not absolute. The suite runs in
        # the repo on the workspace substrate and in an ephemeral copy of it
        # on the jail substrate, and only the former is a path the caller
        # knows. Coverage.result hands back absolute paths, so an absolute
        # report was aligned against the repo directory — correct on one
        # substrate and a guaranteed "NONE under the repo root" on the
        # default one. Relative to cwd is repo-relative on both. Files
        # outside cwd (gems, stdlib) are dropped here; they are never
        # candidates.
        rel = begin
          Pathname.new(path).relative_path_from(Pathname.pwd).to_s
        rescue ArgumentError
          next
        end
        next if rel == '..' || rel.start_with?('../')
        out.puts "#{hit} #{rel}"
      end
    end
  rescue StandardError => e
    # Never let the reporter's own failure change the suite's exit code —
    # the caller distinguishes "could not measure" from "measured nothing",
    # and an absent report already means the former.
    warn "corral: ruby coverage reporter failed: #{e}"
  end
end
at_exit do
  next if CorralCov.written?
  begin
    CorralCov.write(Coverage.peek_result)
  rescue StandardError => e
    warn "corral: ruby coverage reporter failed: #{e}"
  end
end
`

// CoverageCmd wraps a Ruby test command in stdlib coverage instrumentation.
//
// The `;` + explicit rc check between the run and the reduction is deliberate
// and matches goPlugin.CoverageCmd: a suite with FAILING TESTS is the single
// most likely state of a repository corral is auditing, and `&&` would discard
// the report precisely there. Coverage data is complete whether the suite
// passed or failed. Only 0 and 1 fall through — anything else (a bad flag, a
// signal) re-raises, leaving stdout non-conforming, which ParseCoverage turns
// into an error rather than a silent empty map.
func (rubyPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	if len(testCmd) == 0 {
		return nil, false
	}
	// `bundle exec <runner>` is a ruby process; `bundle install` is not, so
	// bundle alone is not a runner token.
	if !coverageRunnerNamed(testCmd, []string{"ruby", "rspec", "rake", "testrb"}) {
		if !(len(testCmd) >= 2 && filepath.Base(testCmd[0]) == "bundle" && testCmd[1] == "exec") {
			return nil, false
		}
	}
	setup := `mkdir -p "$d/cov" && cat > "$d/corral_cov.rb" <<'CORRAL_RB_EOF'` + "\n" + rubyCoveragePreload + "CORRAL_RB_EOF\n"
	env := `CORRAL_COV_DIR="$d/cov" RUBYOPT="-r$d/corral_cov ${RUBYOPT:-}"`
	return coverageRunAndReduce(setup, env, testCmd, coverageMergeDir(rubyCoverageHeader)), true
}

// ParseCoverage reads the reduced report rubyCoveragePreload writes. The
// grammar and its tri-state are shared with JavaScript — see
// corralCoverageReport.
func (rubyPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	return corralCoverageReport(stdout, rubyCoverageHeader, "ruby", modulePath)
}
