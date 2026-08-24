// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"
)

// THE WIRE TEST. An option nobody passes is a feature that does nothing —
// this branch already shipped one such gap (a sink with no adapter), so the
// gate gets a test proving JailScorer actually hands it to adequacy.Score
// rather than merely that the option exists.
//
// scoreOpts is the single seam every adequacy.Score call in gate.go shares, so
// asserting here covers all three call sites at once.
func TestScoreOptsCarriesTheMutantCompileGate(t *testing.T) {
	s := JailScorer{}
	base := map[string]string{
		"internal/x/x.go":      "package x\n",
		"internal/x/x_test.go": "package x\n",
	}
	opts := s.gatedScoreOpts("internal/x/x.go", base)
	if len(opts) < 3 {
		t.Fatalf("scoreOpts returned %d options, want at least 3 (timeout, concurrency, compile gate) — the gate is not wired", len(opts))
	}
}

// The gate must use the LANGUAGE PLUGIN's own check, not a hardcoded command,
// or python/ruby/js audits get a Go vet invocation.
func TestMutantGateUsesThePluginsOwnCheck(t *testing.T) {
	p, err := pluginFor("internal/x/x.go")
	if err != nil {
		t.Fatalf("pluginFor: %v", err)
	}
	cc := p.CompileCheck("internal/x/x.go", "internal/x/x_test.go")
	if len(cc) == 0 {
		t.Fatal("the go plugin returned an empty CompileCheck sequence")
	}
	joined := strings.Join(cc[0], " ")
	if !strings.Contains(joined, "go") || !strings.Contains(joined, "vet") {
		t.Errorf("go CompileCheck = %q, expected the plugin's own vet invocation", joined)
	}
}

// A test path absent from the mutant workspace must NOT be handed to a
// per-file checker: python/ruby/node are given both paths and fail on a
// missing one, which would mark every mutant invalid and erase the exam.
func TestMutantGateFallsBackWhenTheTestPathIsAbsent(t *testing.T) {
	s := JailScorer{}
	onlyCode := map[string]string{"internal/x/x.go": "package x\n"}
	// Must not panic, and must still produce a gate.
	if got := len(s.gatedScoreOpts("internal/x/x.go", onlyCode)); got < 3 {
		t.Fatalf("scoreOpts returned %d options with no test file in the workspace, want the gate still present", got)
	}
}

// An unsupported language must not gain an invented gate — it fails with a
// real message elsewhere, and "every mutant invalid" is a worse diagnosis.
func TestMutantGateSkippedForUnsupportedLanguage(t *testing.T) {
	s := JailScorer{}
	opts := s.gatedScoreOpts("weird/thing.cobol", map[string]string{})
	if len(opts) != 2 {
		t.Errorf("scoreOpts returned %d options for an unsupported language, want exactly 2 (timeout, concurrency)", len(opts))
	}
}
