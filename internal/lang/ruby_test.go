// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

func TestRubyPlugin(t *testing.T) {
	p, ok := ByName("ruby")
	if !ok {
		t.Fatal("ruby plugin not registered")
	}
	if !p.Detect("app/pricing.rb") || p.Detect("app/pricing.py") {
		t.Fatal("Detect must match .rb only")
	}
	if got := pairing.Candidates(p.TestRules(), "app/pricing.rb")[0]; got.Path != "app/pricing_test.rb" || got.Rank != 0 {
		t.Fatalf("Candidates()[0] = %+v, want {app/pricing_test.rb, 0}", got)
	}
	if got := pairing.Candidates(p.TestRules(), "pricing.rb")[0]; got.Path != "pricing_test.rb" || got.Rank != 0 {
		t.Fatalf("Candidates()[0] = %+v, want {pricing_test.rb, 0}", got)
	}
	// A two-command SEQUENCE, not a single `&&`-joined argv element: `ruby -c`
	// only checks one file per invocation, and a bare `&&` argv element only
	// means anything to a shell — the workspace substrate execs argv directly
	// with no shell to interpret it.
	cc := p.CompileCheck("pricing.rb", "pricing_test.rb")
	if len(cc) != 3 || !reflect.DeepEqual(cc[:2], [][]string{{"ruby", "-c", "pricing.rb"}, {"ruby", "-c", "pricing_test.rb"}}) ||
		cc[2][0] != "ruby" || cc[2][len(cc[2])-1] != "pricing.rb" {
		t.Fatalf("CompileCheck = %v", cc)
	}
	// TestCmd MUST invoke a shell EXPLICITLY. Smuggling the script into argv[0]
	// only worked on the jail substrate, which shell-joins argv; the workspace
	// substrate execs argv directly and tried to run a program literally named
	// `t="$(ls`, which is how the first real rubocop audit died.
	if cmd := p.TestCmd(); len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("TestCmd must be an explicit sh -c invocation, got %v", cmd)
	}
	tc := p.TestCmd()[2]
	if !strings.Contains(tc, "rspec") || !strings.Contains(tc, "ruby ") {
		t.Fatalf("TestCmd must dispatch rspec-or-ruby: %q", tc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "minitest") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("ruby system prompts must be language-appropriate")
	}
	if p.PromptLang() != "Ruby" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestRubyCandidatesOrder pins the ordered-candidate-list contract for the
// lib/ vs test/ (or spec/) layout: sibling first, then the leading directory
// (conventionally "lib") replaced by test/, then the RSpec equivalent.
func TestRubyCandidatesOrder(t *testing.T) {
	p, _ := ByName("ruby")
	cases := []struct {
		name string
		in   string
		want []pairing.Candidate
	}{
		{
			name: "top-level file",
			in:   "pricing.rb",
			want: []pairing.Candidate{
				{Path: "pricing_test.rb", Rank: 0},
				{Path: "test/pricing_test.rb", Rank: 1},
				{Path: "spec/pricing_spec.rb", Rank: 1},
				{Path: "test/test_pricing.rb", Rank: 2},
			},
		},
		{
			name: "single-segment lib dir",
			in:   "lib/foo.rb",
			want: []pairing.Candidate{
				{Path: "lib/foo_test.rb", Rank: 0},
				{Path: "test/foo_test.rb", Rank: 1},
				{Path: "spec/foo_spec.rb", Rank: 1},
				{Path: "test/test_foo.rb", Rank: 2},
			},
		},
		{
			// minitest's own house style: test/<sub>/test_<base>.rb, the
			// PREFIX form. Measured on minitest/minitest at depth 20: the
			// suffix forms above pair 0 of 24 files, the prefix form pairs 4.
			name: "nested lib dir, minitest prefix form",
			in:   "lib/minitest/server.rb",
			want: []pairing.Candidate{
				{Path: "lib/minitest/server_test.rb", Rank: 0},
				{Path: "test/minitest/server_test.rb", Rank: 1},
				{Path: "spec/minitest/server_spec.rb", Rank: 1},
				{Path: "test/minitest/test_server.rb", Rank: 2},
			},
		},
		{
			name: "lib/<pkg> layout",
			in:   "lib/mypkg/foo.rb",
			want: []pairing.Candidate{
				{Path: "lib/mypkg/foo_test.rb", Rank: 0},
				{Path: "test/mypkg/foo_test.rb", Rank: 1},
				{Path: "spec/mypkg/foo_spec.rb", Rank: 1},
				{Path: "test/mypkg/test_foo.rb", Rank: 2},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pairing.Candidates(p.TestRules(), c.in)
			if len(got) != len(c.want) {
				t.Fatalf("Candidates(%q) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i].Path != c.want[i].Path || got[i].Rank != c.want[i].Rank {
					t.Errorf("Candidates(%q)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// RUBY COUNTS COME FROM A REAL RUN of the fixture suite (the captured report a
// live run is held to). dead.rb is the negative control: its `def` lines run
// when it is required, so line counts alone would credit it — a line counts
// as executed only inside a method that was called.
func TestRubyCoverageCountsFromRealRun(t *testing.T) {
	assertFixtureCounts(t, capturedCounts(t, "ruby"), "lib/calc.rb", "lib/dead.rb", "lib/unloaded.rb", "lib/empty.rb")
}

// THE RUBY PRELOAD DROPS A FILE OUTSIDE THE WORKING DIRECTORY BEFORE IT
// COUNTS IT, as the PHP prepend does (ruling R30, final review T3). A gem or
// stdlib file is never a candidate, so counting it was at_exit work done in
// the operator's own process for nothing — and any surprise in its data
// (here, line data that is not a list) failed the whole report.
func TestRubyPreloadNeverCountsAFileOutsideTheWorkingDirectory(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not installed")
	}
	dir := t.TempDir()
	preload := filepath.Join(dir, "corral_cov.rb")
	if err := os.WriteFile(preload, []byte(rubyCoveragePreload), 0o600); err != nil {
		t.Fatal(err)
	}
	cov := filepath.Join(dir, "cov")
	if err := os.Mkdir(cov, 0o750); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	script := `CorralCov.write({"/outside/gem.rb" => {lines: :not_a_list, methods: {}}, File.join(Dir.pwd, "a.rb") => {lines: [1, nil], methods: {}}})`
	c := exec.Command("ruby", "-r"+preload, "-e", script) // #nosec G204 -- ruby and a temp preload this test wrote
	c.Dir = work
	c.Env = append(os.Environ(), "CORRAL_COV_DIR="+cov)
	if out, err := c.CombinedOutput(); err != nil || strings.Contains(string(out), "reporter failed") {
		t.Fatalf("ruby: %v\n%s", err, out)
	}
	entries, _ := os.ReadDir(cov)
	var report strings.Builder
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(cov, e.Name())) // #nosec G304 -- a temp file this test's process wrote
		report.Write(b)
	}
	if !strings.Contains(report.String(), "1 1 a.rb") || strings.Contains(report.String(), "gem.rb") {
		t.Errorf("report = %q, want a.rb counted and the out-of-cwd gem never read", report.String())
	}
}
