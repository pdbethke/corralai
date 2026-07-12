// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/controlspec"
)

// runControl implements `corral control seed` — write one vetted GateTest (+
// its workspace recipe) into the controlspec store so the control gate has a
// real control to run. It goes through SaveCandidate (forces unvetted) then
// Promote (vets), the same candidate→vetted human-gate path a control owner
// uses — never a back door around vetting.
func runControl(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "seed" {
		return fmt.Errorf("usage: corral control seed --spec-db <path> --owner <principal> --goal <id> --target <repo-path> --code-path <flat> --test-path <flat> --test-file <path> [--kill-rate <float>]")
	}
	fs := flag.NewFlagSet("control seed", flag.ContinueOnError)
	specDB := fs.String("spec-db", "", "controlspec DuckDB path")
	owner := fs.String("owner", "", "control-owner principal")
	goal := fs.String("goal", "", "goal id")
	target := fs.String("target", "", "repo-relative target file path")
	codePath := fs.String("code-path", "", "flat target filename in the jail workspace")
	testPath := fs.String("test-path", "", "flat test filename in the jail workspace")
	testFile := fs.String("test-file", "", "path to the vetted test source file")
	killRate := fs.Float64("kill-rate", 1.0, "recorded adequacy kill rate")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *specDB == "" || *owner == "" || *goal == "" || *target == "" || *codePath == "" || *testPath == "" || *testFile == "" {
		return fmt.Errorf("all of --spec-db --owner --goal --target --code-path --test-path --test-file are required")
	}
	if *codePath == *testPath {
		return fmt.Errorf("--code-path and --test-path must differ (the test would overwrite the code in the workspace)")
	}
	if !strings.HasSuffix(*testPath, "_test.go") {
		return fmt.Errorf("--test-path must end in _test.go, else `go test` finds no tests and the control passes vacuously: %q", *testPath)
	}
	src, err := os.ReadFile(*testFile) // #nosec G304 -- operator-supplied path in a local admin CLI
	if err != nil {
		return fmt.Errorf("read test file: %w", err)
	}
	s, err := controlspec.OpenStore(*specDB)
	if err != nil {
		return err
	}
	defer s.Close()
	now := time.Now().UTC()
	gt := controlspec.GateTest{
		Owner: *owner, Goal: *goal, Target: *target, Test: string(src),
		KillRate: *killRate, CodePath: *codePath, TestPath: *testPath, CreatedTS: now,
	}
	if err := s.SaveCandidate(gt); err != nil {
		return err
	}
	ok, err := s.Promote(*owner, *goal, *target, now)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("promote: no unvetted candidate to vet (already vetted?)")
	}
	fmt.Fprintf(out, "seeded + vetted control %s@%s for %s\n", *goal, *target, *owner)
	return nil
}
