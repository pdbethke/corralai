// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ReasonMappedTestMissing marks a source file the tenant mapped to a test that
// does not exist in the tree.
//
// It is a REFUSAL, never a fallback to convention: falling back would pair the
// file to something the tenant did not choose, and they would have no way to
// see that their mapping had been ignored. A typo must be visible.
const ReasonMappedTestMissing = "mapped-test-missing"

// TestMap is a tenant-supplied source-file → test-file mapping, used when
// convention cannot pair a repository (or pairs it wrongly).
//
// Pairing is convention-based, which works when a project names tests after
// source files and CANNOT work when it does not. expressjs/express is the clean
// case, and why the CI sweep pins it at ZERO candidates rather than inviting a
// looser heuristic:
//
//	lib/application.js -> test/app.js, app.all.js, app.engine.js, app.param.js …
//	lib/response.js    -> test/res.send.js, res.json.js …
//
// `application -> app` and `response -> res` is that project's own shorthand.
// No filename rule derives it, and a rule loose enough to try would pair the
// WRONG files — planting mutants in one file and grading them against another's
// tests, which yields a confident, signed, wrong verdict.
//
// psf/requests is the subtler case: adapters.py DOES pair, to an 8-line
// test_adapters.py, while its real coverage lives in a 108KB test_requests.py.
// Convention found a file; it found the wrong one, and scoping to it inverted
// that file's verdict from 1.00 to 0.00.
//
// The tenant knows their layout. corral cannot infer it, and should stop
// pretending otherwise.
type TestMap struct{ m map[string]string }

// NewFileTestMap reads a JSON object mapping repo-relative source paths to
// repo-relative test paths — deliberately the same shape as --goals, which
// exists for the same reason: derivation is guesswork, and the operator may
// simply know.
func NewFileTestMap(path string) (*TestMap, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied --tests path, same trust class as --goals
	if err != nil {
		return nil, err
	}
	raw := map[string]string{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(raw))
	for src, test := range raw {
		src, test = strings.TrimSpace(src), strings.TrimSpace(test)
		if src == "" || test == "" {
			// An empty mapping is NOT a pairing to nowhere. Pairing a file to
			// "" would grade it against an empty command — green on the
			// baseline AND green on every mutant, a confident 0.00 kill rate
			// that is not an error anywhere in the pipeline.
			continue
		}
		m[filepath.ToSlash(src)] = filepath.ToSlash(test)
	}
	return &TestMap{m: m}, nil
}

// TestFor returns the tenant's mapping for a source path, if any. A path with
// no entry falls through to convention rather than being blocked: a partial map
// is the common case, since a tenant only needs to correct what convention gets
// wrong.
func (t *TestMap) TestFor(srcPath string) (string, bool) {
	if t == nil || len(t.m) == 0 {
		return "", false
	}
	v, ok := t.m[filepath.ToSlash(srcPath)]
	return v, ok
}

// Len reports how many mappings were supplied, for disclosure in the scan
// report — an operator should be able to see that their map was actually read.
func (t *TestMap) Len() int {
	if t == nil {
		return 0
	}
	return len(t.m)
}
