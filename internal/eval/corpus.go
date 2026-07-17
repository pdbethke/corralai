// SPDX-License-Identifier: Elastic-2.0

// Package eval runs the adversarial pool across a versioned, content-addressed
// corpus to give the bug-catching scorecard volume, and validates the metric
// against known-adequacy targets. It adds no judgement — it only triggers
// existing pool runs — and depends on a small PoolRunner interface it owns.
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Target struct {
	ID                string `json:"id"`
	CodePath          string `json:"code_path"`
	TestPath          string `json:"test_path"`
	Goal              string `json:"goal"`
	TestCmd           string `json:"test_cmd"`
	ExpectedAdequacy  string `json:"expected_adequacy"` // thorough | gappy | unknown
	KnownGap          string `json:"known_gap,omitempty"`
	ExpectedSurvivors int    `json:"expected_survivors,omitempty"`
	NMutants          int    `json:"n_mutants,omitempty"`

	code, testCode string // filled at Load, relative to the manifest's directory
}

type Manifest struct {
	CorpusVersion string   `json:"corpus_version"`
	Targets       []Target `json:"targets"`
}

func Load(manifestPath string) (Manifest, error) {
	raw, err := os.ReadFile(manifestPath) // #nosec G304 -- operator-supplied corpus path
	if err != nil {
		return Manifest{}, fmt.Errorf("eval: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("eval: parse manifest: %w", err)
	}
	if m.CorpusVersion == "" {
		return Manifest{}, fmt.Errorf("eval: manifest has no corpus_version")
	}
	base := filepath.Dir(manifestPath)
	seen := map[string]bool{}
	for i := range m.Targets {
		t := &m.Targets[i]
		if t.ID == "" || t.CodePath == "" || t.TestPath == "" || t.Goal == "" || t.TestCmd == "" {
			return Manifest{}, fmt.Errorf("eval: target %d missing a required field", i)
		}
		if seen[t.ID] {
			return Manifest{}, fmt.Errorf("eval: duplicate target id %q", t.ID)
		}
		seen[t.ID] = true
		code, err := os.ReadFile(filepath.Join(base, t.CodePath)) // #nosec G304
		if err != nil {
			return Manifest{}, fmt.Errorf("eval: target %s code %s: %w", t.ID, t.CodePath, err)
		}
		test, err := os.ReadFile(filepath.Join(base, t.TestPath)) // #nosec G304
		if err != nil {
			return Manifest{}, fmt.Errorf("eval: target %s test %s: %w", t.ID, t.TestPath, err)
		}
		t.code, t.testCode = string(code), string(test)
		if t.NMutants == 0 {
			t.NMutants = 8
		}
	}
	return m, nil
}

func (t Target) Code() string     { return t.code }
func (t Target) TestCode() string { return t.testCode }

func (t Target) Digest() string {
	h := sha256.New()
	for _, s := range []string{t.code, t.testCode, t.Goal, t.TestCmd} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
