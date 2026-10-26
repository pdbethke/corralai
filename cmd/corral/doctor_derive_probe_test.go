// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// installDoctorDeriver points doctor's derive probe at a fake backend, through
// the SAME llmDeriver certify uses.
func installDoctorDeriver(t *testing.T, fb *fakeBackend) {
	t.Helper()
	prev := doctorDeriver
	t.Cleanup(func() { doctorDeriver = prev })
	doctorDeriver = func(model, _ string) (reposcan.Deriver, error) { return newMeteredDeriver(fb, model), nil }
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
			r := probeDeriveSeat("claude-code", "")
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
	doctorDeriver = func(string, string) (reposcan.Deriver, error) { called = true; return nil, errors.New("unused") }
	var out bytes.Buffer
	runDoctor([]string{"--repo", t.TempDir()}, &out, &bytes.Buffer{})
	if called || strings.Contains(out.String(), "derive seat answers") {
		t.Errorf("doctor probed a derive seat nobody named:\n%s", out.String())
	}
}
