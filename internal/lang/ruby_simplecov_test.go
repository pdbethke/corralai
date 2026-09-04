// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRubyCoverageSurvivesSimpleCov pins the sixth review's M2: SimpleCov's
// own at_exit calls Coverage.result, which STOPS and CLEARS coverage, and
// corral's hook — registered first via RUBYOPT, so run last — then found
// "coverage measurement is not enabled" on every SimpleCov project, and the
// diagnosis blamed the suite for importing nothing. Coverage.result is now
// wrapped so the first caller's snapshot is what corral reports. Needs
// rspec + simplecov installed for the user; skips otherwise.
func TestRubyCoverageSurvivesSimpleCov(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("no ruby")
	}
	if err := exec.Command("ruby", "-e", "require 'simplecov'; require 'rspec'").Run(); err != nil {
		t.Skip("simplecov/rspec not installed: gem install --user-install simplecov rspec")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "lib"), 0o755)
	os.MkdirAll(filepath.Join(root, "spec"), 0o755)
	os.WriteFile(filepath.Join(root, "lib/calc.rb"), []byte("class Calc; def add(a,b) a+b end end\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lib/unused.rb"), []byte("class Unused; def x; 1 end end\n"), 0o644)
	os.WriteFile(filepath.Join(root, "spec/calc_spec.rb"), []byte("require 'simplecov'\nSimpleCov.start\nrequire_relative '../lib/calc'\nrequire_relative '../lib/unused'\nRSpec.describe Calc do it('adds'){ expect(Calc.new.add(1,2)).to eq 3 } end\n"), 0o644)
	cmd, ok := rubyPlugin{}.CoverageCmd([]string{"rspec"})
	if !ok {
		t.Fatal("no coverage cmd")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir = root
	c.Env = append(os.Environ(), "PATH=/home/pdbethke/.local/share/gem/ruby/3.3.0/bin:"+os.Getenv("PATH"))
	out, err := c.CombinedOutput()
	t.Logf("err=%v\n%s", err, out)
	rep, perr := rubyPlugin{}.ParseCoverage(string(out), root)
	t.Logf("parse err=%v report=%+v", perr, rep)
	if perr != nil {
		t.Fatalf("coverage report failed under SimpleCov: %v", perr)
	}
	if hit, ok := rep["lib/calc.rb"]; !ok || !hit {
		t.Errorf("lib/calc.rb should be executed: %+v", rep)
	}
	if hit, ok := rep["lib/unused.rb"]; !ok || hit {
		t.Errorf("lib/unused.rb is required but never called and should read false: %+v", rep)
	}
}
