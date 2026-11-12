// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/models"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// installDoctorDeriver points doctor's derive probe at a fake backend, through
// the SAME llmDeriver certify uses.
func installDoctorDeriver(t *testing.T, fb *fakeBackend) {
	t.Helper()
	prev := doctorDeriver
	t.Cleanup(func() { doctorDeriver = prev })
	doctorDeriver = func(rs agentbackend.ResolvedSeat) (reposcan.Deriver, error) {
		return newMeteredDeriver(fb, rs.Spec), nil
	}
}

// THE DERIVE PROBE (ruling R25): one small real derive call through
// certify's own deriver, reported by outcome. A goal or an explicit NONE
// passes; an empty reply or a seat error fails, since either one is what
// excluded an evidence-paired file as "purely declarative" in B6's audit.
func TestDoctorDeriveProbeReportsEachOutcome(t *testing.T) {
	for _, c := range []struct {
		name   string
		fb     *fakeBackend
		ok     bool
		detail []string
	}{
		{"goal", &fakeBackend{reply: "Clamp never returns a value outside [lo, hi].\nmore", usage: agentbackend.Usage{InputTokens: 120, OutputTokens: 14}}, true,
			[]string{"goal: Clamp never returns a value outside [lo, hi].", "120 input / 14 output tokens", "spent one call"}},
		{"none", &fakeBackend{reply: "NONE", usage: agentbackend.Usage{InputTokens: 120, OutputTokens: 1}}, true,
			[]string{"answered NONE", "ungoaled"}},
		{"empty", &fakeBackend{reply: "  "}, false, []string{"empty reply"}},
		{"seat error", &fakeBackend{err: errors.New("claude-code: login expired")}, false, []string{"login expired"}},
		{"not metered", &fakeBackend{reply: "Clamp bounds v.", usage: agentbackend.Usage{Unmetered: true}}, true, []string{"not metered"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			installDoctorDeriver(t, c.fb)
			r := probeDeriveSeat(agentbackend.ResolvedSeat{Flag: "derive-model", Spec: "claude-code", Kind: "agent", DeclaredBy: agentbackend.DeclaredAgent})
			if r.ok != c.ok {
				t.Errorf("ok = %v, want %v: %+v", r.ok, c.ok, r)
			}
			if !strings.Contains(r.name, "claude-code") || !strings.Contains(r.name, "one call") {
				t.Errorf("the check's name must name the seat and say it spends one call: %q", r.name)
			}
			for _, d := range c.detail {
				if !strings.Contains(r.detail, d) {
					t.Errorf("detail %q is missing %q", r.detail, d)
				}
			}
			if c.fb.err == nil && len(c.fb.sent) != 2 {
				t.Errorf("the probe must send certify's two-message derive prompt, sent %d", len(c.fb.sent))
			}
		})
	}
}

// Nothing is probed unless --derive-model is named: doctor's other checks
// stay free.
func TestDoctorProbesNothingWithoutADeriveModel(t *testing.T) {
	called := false
	prev := doctorDeriver
	t.Cleanup(func() { doctorDeriver = prev })
	doctorDeriver = func(agentbackend.ResolvedSeat) (reposcan.Deriver, error) {
		called = true
		return nil, errors.New("unused")
	}
	var out bytes.Buffer
	runDoctor([]string{"--repo", t.TempDir()}, &out, &bytes.Buffer{})
	if called || strings.Contains(out.String(), "derive seat answers") {
		t.Errorf("doctor probed a derive seat nobody named:\n%s", out.String())
	}
}

// THE SPENT CALL IS ALWAYS SHOWN (review I1). A fatal check (here an unknown
// sandbox backend) stops the other checks' output; the derive probe still
// says it is spending a call, before making it, and prints what came back.
func TestDoctorShowsTheDeriveProbeAfterAFatalCheck(t *testing.T) {
	// The derive seat's bare local name needs a declared source like any seat.
	t.Setenv("OLLAMA_URL", "http://ollama.test.invalid:11434")
	t.Setenv("MODEL_BACKEND", "")
	fakeOllama(t, "llama3.2:3b")
	installDoctorDeriver(t, &fakeBackend{reply: "Clamp bounds v.", usage: agentbackend.Usage{InputTokens: 10, OutputTokens: 4}})
	var out bytes.Buffer
	runDoctor([]string{"--repo", t.TempDir(), "--jail", "no-such-backend", "--derive-model", "llama3.2:3b"}, &out, &bytes.Buffer{})
	s := out.String()
	if !strings.Contains(s, "later checks skipped") {
		t.Fatalf("fixture: the sandbox check must be fatal:\n%s", s)
	}
	announce := strings.Index(s, "spending one derive call on llama3.2:3b")
	result := strings.Index(s, "derive seat answers (llama3.2:3b)")
	if announce < 0 || result < announce || !strings.Contains(s, "goal: Clamp bounds v.") {
		t.Errorf("the probe must be announced, then reported, after a fatal check:\n%s", s)
	}
}

// Doctor rehearses the derive seat from the SAME resolved routing certify
// builds it from: a registry seat on an openai-chat daemon passes with no
// cloud credential in scope, and its failure to build is reported as the
// derive seat's, not as a missing vendor key.
func TestDoctorChecksTheDeriveSeatFromItsResolvedRouting(t *testing.T) {
	noKeys(t)
	rs := agentbackend.ResolvedSeat{
		Flag: "derive-model", Spec: "qwen3-coder", Kind: "api", Provider: "vllm",
		Wire: models.WireOpenAIChat, Endpoint: "http://gpu2.internal:8000/v1", DeclaredBy: agentbackend.DeclaredRegistry,
	}
	if res := checkDeriveSeat(rs); len(res) != 1 || !res[0].ok {
		t.Fatalf("a resolved openai-chat derive seat failed doctor's rehearsal: %+v", res)
	}
	rs.Endpoint = ""
	res := checkDeriveSeat(rs)
	if len(res) != 1 || res[0].ok || !strings.Contains(res[0].detail, "no endpoint was resolved") {
		t.Fatalf("a seat with no resolved endpoint must fail naming that: %+v", res)
	}
}
