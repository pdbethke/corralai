// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// scriptedIsolator answers the self-test's probe with a fixed shell script,
// so the parsing and the failure messages are tested without a real jail.
type scriptedIsolator struct {
	name  string
	reply string // what the "jail" prints
	calls *int
}

func (s scriptedIsolator) Name() string             { return s.name }
func (s scriptedIsolator) Preflight() error         { return nil }
func (s scriptedIsolator) jailDirs(string) jailDirs { return linuxJailDirs }
func (s scriptedIsolator) netDenialProbe() string   { return "false" }
func (s scriptedIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if s.calls != nil {
		*s.calls++
	}
	return []string{"/bin/sh", "-c", "printf '%s' " + shQuote(s.reply)}, nil
}

func TestSelfTestPassesWhenEveryCheckReports(t *testing.T) {
	iso := scriptedIsolator{name: "fake", reply: strings.Join(selfTestChecks, "\n") + "\n"}
	if err := selfTest(iso); err != nil {
		t.Fatalf("selfTest = %v, want nil", err)
	}
}

func TestSelfTestNamesTheFirstFailedCheck(t *testing.T) {
	iso := scriptedIsolator{name: "fake", reply: "sh-starts\nworkspace-write\n"}
	err := selfTest(iso)
	var ste *SelfTestError
	if !errors.As(err, &ste) {
		t.Fatalf("err = %v, want *SelfTestError", err)
	}
	if ste.Check != "env-pins" || ste.Backend != "fake" {
		t.Errorf("Check=%q Backend=%q, want env-pins/fake", ste.Check, ste.Backend)
	}
	if !strings.Contains(err.Error(), "fake jail self-test failed at env-pins on ") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestSelfTestRunsOncePerBackend(t *testing.T) {
	resetSelfTestCache()
	t.Cleanup(resetSelfTestCache)
	calls := 0
	iso := scriptedIsolator{name: "fake-cached", reply: strings.Join(selfTestChecks, "\n") + "\n", calls: &calls}
	for i := 0; i < 3; i++ {
		if err := cachedSelfTest(iso); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("the self-test ran %d times, want once per process", calls)
	}
}

func TestSelfTestCachesFailureToo(t *testing.T) {
	resetSelfTestCache()
	t.Cleanup(resetSelfTestCache)
	calls := 0
	iso := scriptedIsolator{name: "fake-broken", reply: "", calls: &calls}
	e1, e2 := cachedSelfTest(iso), cachedSelfTest(iso)
	if e1 == nil || e2 == nil || calls != 1 {
		t.Errorf("e1=%v e2=%v calls=%d, want the same failure, computed once", e1, e2, calls)
	}
}
