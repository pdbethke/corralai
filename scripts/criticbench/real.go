// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var goTestFunc = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)

// realFixture loads a real Go source file and the _test.go beside it as a
// fixture with NO answer key. Nobody has checked which of its tests can never
// fail, so it is marked unkeyed and the bench reports only what each mode
// flagged, how stably, and at what cost; a test it names that is not in the
// file still counts as false, since that one needs no key.
//
// The goal is generic because a real file has none written for it: the critic
// is asked to judge the tests against the code's own doc comments.
func realFixture(src string) (fixture, error) {
	code, err := os.ReadFile(src) // #nosec G304 -- an operator-named file in a dev-only bench
	if err != nil {
		return fixture{}, err
	}
	testPath := strings.TrimSuffix(src, ".go") + "_test.go"
	tests, err := os.ReadFile(testPath) // #nosec G304 -- derived from the operator-named file
	if err != nil {
		return fixture{}, fmt.Errorf("%s has no paired test file: %w", src, err)
	}
	var all []string
	for _, m := range goTestFunc.FindAllStringSubmatch(string(tests), -1) {
		all = append(all, m[1])
	}
	return fixture{
		name:     filepath.Base(filepath.Dir(src)) + "/" + filepath.Base(src),
		goal:     "the behaviour the doc comments in " + filepath.Base(src) + " describe",
		codePath: src, code: string(code), testPath: testPath, tests: string(tests),
		all: all, unkeyed: true,
	}, nil
}
