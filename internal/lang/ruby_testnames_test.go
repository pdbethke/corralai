// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// rspec: each example by its groups' full description and its own, in
// idFromFull's form ("Calc neg::adds"), the id rspec lists it under.
func TestRubyTestNamesInSourceRSpec(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		{"do blocks", "RSpec.describe Calc do\n  it \"adds\" do\n    expect(1).to eq 1\n  end\nend\n", []string{"Calc::adds"}},
		{"nested context", "RSpec.describe Calc do\n  context 'neg' do\n    it 'adds' do\n    end\n  end\nend\n", []string{"Calc neg::adds"}},
		{"brace blocks", "RSpec.describe('Calc') do\n  it('adds') { expect(1).to eq 1 }\n  describe('neg') { it('adds') {} }\nend\n", []string{"Calc::adds", "Calc neg::adds"}},
		{"method group joins without a space", "RSpec.describe Calc do\n  describe '#add' do\n    it 'sums' do\n    end\n  end\n  describe '.build' do\n    it 'makes' do; end\n  end\nend\n",
			[]string{"Calc#add::sums", "Calc.build::makes"}},
		{"constant path", "RSpec.describe MyApp::Calc do\n  it 'adds' do\n  end\nend\n", []string{"MyApp::Calc::adds"}},
		{"constant and string", "describe Calc, 'math' do\n  it 'adds' do\n  end\nend\n", []string{"Calc math::adds"}},
		{"symbol", "describe :calc do\n  it 'adds' do\n  end\nend\n", []string{"calc::adds"}},
		{"skipped and focused listed", "describe 'G' do\n  xit 'a' do\n  end\n  fit 'b' do\n  end\n  specify 'c' do\n  end\n  it 'd'\n  pending 'e' do\n  end\nend\n",
			[]string{"G::a", "G::b", "G::c", "G::d", "G::e"}},
		{"metadata after the title", "describe 'G', :slow do\n  it 'a', focus: true do\n  end\nend\n", []string{"G::a"}},
		{"bodies are not read", "describe 'G' do\n  before { skip 'later' if ENV['X'] }\n  let(:x) { {a: 1} }\n  it 'a' do\n    skip 'not yet'\n    expect(x).to eq({a: 1})\n    if x then y end\n    [1, 2].each do |n|\n      n\n    end\n  end\n  def helper\n    1\n  end\nend\n",
			[]string{"G::a"}},
		{"comments and strings", "# it 'fake' do\ndescribe 'G' do\n  it 'has an end in it' do\n    s = \"end }\"\n    t = 'do'\n  end\nend\n", []string{"G::has an end in it"}},
		{"heredoc", "describe 'G' do\n  it 'a' do\n    s = <<~TXT\n      it 'fake' do\n      end\n    TXT\n  end\nend\n", []string{"G::a"}},
		{"modifier if", "describe 'G' do\n  it 'a' do\n    return if x\n    y = 1 unless z\n  end\nend\n", []string{"G::a"}},
		{"spec_ prefix", "describe 'G' do\n  it 'a' do\n  end\nend\n", []string{"G::a"}},
	} {
		path := "spec/calc_spec.rb"
		if c.name == "spec_ prefix" {
			path = "spec/spec_calc.rb"
		}
		if got := rubyTestNames(path, c.src); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// minitest: Class#test_name, minitest's own label for a test.
func TestRubyTestNamesInSourceMinitest(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		{"methods", "class CalcTest < Minitest::Test\n  def test_adds\n    assert true\n  end\n  def helper; end\n  def test_subs; end\nend\n", []string{"CalcTest#test_adds", "CalcTest#test_subs"}},
		{"private is not run", "class CalcTest < Minitest::Test\n  def test_a; end\n  private\n  def test_hidden; end\nend\n", []string{"CalcTest#test_a"}},
		{"private def", "class CalcTest < Minitest::Test\n  private def test_hidden; end\n  def test_a; end\nend\n", []string{"CalcTest#test_a"}},
		{"module nesting", "module MyApp\n  class NegTest < Minitest::Test\n    def test_x; end\n  end\nend\n", []string{"MyApp::NegTest#test_x"}},
		{"activesupport test blocks", "class CalcTest < ActiveSupport::TestCase\n  test \"adds two\" do\n    assert true\n  end\n  test 'subs' do\n  end\nend\n", []string{"CalcTest#test_adds_two", "CalcTest#test_subs"}},
		{"not a test class", "class Helper\n  def test_x; end\nend\nclass CalcTest < Minitest::Test\n  def test_a; end\nend\n", []string{"CalcTest#test_a"}},
	} {
		if got := rubyTestNames("test/calc_test.rb", c.src); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := rubyTestNames("test/test_calc.rb", "class CalcTest < Minitest::Test\n  def test_a; end\nend\n"); !reflect.DeepEqual(got, []string{"CalcTest#test_a"}) {
		t.Errorf("test_ prefix: got %q", got)
	}
}

// Any test the lister cannot name exactly lists NOTHING.
func TestRubyTestNamesInSourceFailsClosed(t *testing.T) {
	for _, c := range []struct{ name, path, src string }{
		{"example without a description", "a_spec.rb", "describe 'G' do\n  it { is_expected.to be_nil }\nend\n"},
		{"interpolated title", "a_spec.rb", "describe 'G' do\n  it \"adds #{n}\" do\n  end\nend\n"},
		{"shared examples", "a_spec.rb", "describe 'G' do\n  it_behaves_like 'a thing'\nend\n"},
		{"include examples", "a_spec.rb", "describe 'G' do\n  include_examples 'x'\nend\n"},
		{"generated in a loop", "a_spec.rb", "describe 'G' do\n  [1, 2].each do |n|\n    it 'x' do\n    end\n  end\nend\n"},
		{"variable title", "a_spec.rb", "describe 'G' do\n  it name do\n  end\nend\n"},
		{"unterminated heredoc", "a_spec.rb", "describe 'G' do\n  it 'a' do\n    s = <<~TXT\n  end\nend\n"},
		{"unterminated string", "a_spec.rb", "describe 'G' do\n  it 'a do\n  end\nend\n"},
		{"unbalanced end", "a_spec.rb", "describe 'G' do\n  it 'a' do\n  end\n"},
		{"minitest spec in a _test.rb", "a_test.rb", "describe Calc do\n  it 'adds' do\n  end\nend\n"},
		{"test method in a loop", "a_test.rb", "class CalcTest < Minitest::Test\n  [1, 2].each do |n|\n    define_method(\"test_#{n}\") {}\n    def test_x; end\n  end\nend\n"},
		{"interpolated test block", "a_test.rb", "class CalcTest < ActiveSupport::TestCase\n  test \"adds #{n}\" do\n  end\nend\n"},
		{"test block with punctuation", "a_test.rb", "class CalcTest < ActiveSupport::TestCase\n  test 'adds, then subs' do\n  end\nend\n"},
		{"inherits a test class from this file", "a_test.rb", "class BaseTest < Minitest::Test\n  def test_a; end\nend\nclass MoreTest < BaseTest\n  def test_b; end\nend\n"},
		{"example outside any group", "a_spec.rb", "it 'loose' do\nend\n"},
		{"constant described inside a module", "a_spec.rb", "module MyApp\n  RSpec.describe Calc do\n    it 'b' do\n    end\n  end\nend\n"},
		{"constant aliased in this file", "a_spec.rb", "Alias = MyApp::Calc\nRSpec.describe Alias do\n  it 'c' do\n  end\nend\n"},
		{"neither convention", "calc.rb", "describe 'G' do\n  it 'a' do\n  end\nend\n"},
	} {
		if got := rubyTestNames(c.path, c.src); got != nil {
			t.Errorf("%s: want nil, got %q", c.name, got)
		}
	}
}

// Checked against the real runners (testdata/runners, PROVENANCE.txt):
// rspec's names resolve one to one onto its captured --dry-run list, and
// minitest's names include every test its captured run failed, by the label
// minitest printed.
func TestRubyNamesResolveOntoRealRunnerLists(t *testing.T) {
	dir := filepath.Join("testdata", "runners")
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	rb, _ := ByName("ruby")
	rspec, err := RunnerByID(rb, "rspec")
	if err != nil {
		t.Fatal(err)
	}
	assertNamesResolveOneToOne(t, "spec/calc_spec.rb", rspec,
		rubyTestNames("spec/calc_spec.rb", read("src/spec/calc_spec.rb")), rspec.ParseList(read("rspec-list.json")))

	// How rspec joins descriptions, captured from a real --dry-run: no space
	// before a "#", "." or "::" child only when the parent's last argument is
	// a Module, and always a space before an example's description.
	assertNamesResolveOneToOne(t, "spec/join_spec.rb", rspec,
		rubyTestNames("spec/join_spec.rb", read("src/spec/join_spec.rb")), rspec.ParseList(read("rspec-join-list.json")))

	names := rubyTestNames("fail/f_test.rb", read("src/fail/f_test.rb"))
	failed := minitestFailures(read("minitest-fail.txt"))
	if len(failed) == 0 {
		t.Fatal("the captured minitest run names no failure")
	}
	for _, f := range failed {
		if !slices.Contains(names, f) {
			t.Errorf("minitest failed %q, which the lister did not name (%q)", f, names)
		}
	}
	if want := []string{"CalcTest#test_adds", "CalcTest#test_ok", "CalcTest#test_boom", "MyApp::NegTest#test_x"}; !reflect.DeepEqual(names, want) {
		t.Errorf("got %q, want %q", names, want)
	}
}

// The Ruby plugin answers with the lister.
func TestRubyPluginListsTests(t *testing.T) {
	p, _ := ByName("ruby")
	if got := p.TestNamesInSource("spec/a_spec.rb", "describe 'G' do\n  it 'a' do\n  end\nend\n"); !reflect.DeepEqual(got, []string{"G::a"}) {
		t.Errorf("got %q", got)
	}
}
