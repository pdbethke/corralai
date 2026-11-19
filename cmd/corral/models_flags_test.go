// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// seatFlagLiteral matches the registration of a door's primary seat flag. A
// door that seats a model can be handed a pool, so every such door must
// register --models through registerModelsFlags: derived from the source, not
// a list of commands, so a sixth door is found the day it is written.
var seatFlagLiteral = regexp.MustCompile(`\.String(Var)?\([^)]*"(writer|reviewer)-model"`)

// modelsFlagLiteral matches a hand registration of --models, in any of the
// shapes the flag package offers.
var modelsFlagLiteral = regexp.MustCompile(`\.(String|StringVar|Func|Var)\([^)]*"models"`)

func TestModelsFlagPatternsMatchARegistration(t *testing.T) {
	// Negative controls: each pattern must catch the shape it exists to
	// catch, or the door test below passes vacuously.
	for re, src := range map[*regexp.Regexp]string{
		seatFlagLiteral:   `w := fs.String("writer-model", "", "h")`,
		modelsFlagLiteral: `p := fs.String("models", "", "h")`,
	} {
		if !re.MatchString(src) {
			t.Fatalf("%v no longer matches %q", re, src)
		}
	}
	if !seatFlagLiteral.MatchString(`fs.StringVar(&f.model, "reviewer-model", "", "h")`) {
		t.Fatal("seatFlagLiteral no longer matches review's StringVar registration")
	}
}

func TestEveryDoorWithSeatFlagsRegistersModelsFlags(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	doors := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f) // #nosec G304 -- this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if f != "models_pool.go" {
			if loc := modelsFlagLiteral.FindStringIndex(src); loc != nil {
				t.Errorf("%s registers --models by hand (%q); use registerModelsFlags", f, src[loc[0]:loc[1]])
			}
		}
		if !seatFlagLiteral.MatchString(src) {
			continue
		}
		doors++
		wired := strings.Contains(src, "registerModelsFlags(")
		if !wired {
			t.Errorf("%s registers a seat flag and not --models; call registerModelsFlags", f)
		}
		// Registered is not used: a door that parses --models and never
		// hands it to seatFromPool (or, for demo, to the command it
		// forwards to) accepts a pool and silently ignores it.
		if wired && !strings.Contains(src, "seatFromPool(") && !strings.Contains(src, `"--models"`) {
			t.Errorf("%s registers --models and neither calls seatFromPool nor forwards \"--models\"", f)
		}
	}
	// certify --local, certify --repo, doctor, demo and review.
	if doors < 5 {
		t.Fatalf("found %d doors with a seat flag, want at least 5: the pattern has stopped matching", doors)
	}
}

// Choosing a seat from recorded evidence happens in one place. A door that
// imported the proposer or the ranking itself could seat a model by a rule
// of its own, and the "a pool is not a default" guarantees live in
// seatFromPool.
func TestOnlyAutoseatDecidesASeatFromEvidence(t *testing.T) {
	allowed := map[string]map[string]bool{
		`"github.com/pdbethke/corralai/internal/autoseat"`: {"models_pool.go": true},
		// models_rank.go is the disclosure verb; review.go reads the
		// ranking's seat names and predates the proposer.
		`"github.com/pdbethke/corralai/internal/modelrank"`: {"models_pool.go": true, "models_rank.go": true, "review.go": true},
		// The evidence readers and the ranking itself. An import gate alone
		// misses a door that reads the evidence with the package's own
		// loader and ranks it, so the calls are held to their places too.
		// main.go hands defaultRankLoader to `corral models rank`, which
		// discloses and seats nothing; models_pool.go wires it into
		// doorPoolDeps, the one reader seatFromPool uses; models_rank.go
		// defines both loaders and runs the disclosure verb's ranking.
		"defaultRankLoader":          {"main.go": true, "models_pool.go": true, "models_rank.go": true},
		"machineLedgerRankEvidence(": {"models_rank.go": true},
		// models_pool.go ranks for the proposer (rankPool); models_rank.go
		// ranks for the disclosure verb.
		"modelrank.Rank(": {"models_pool.go": true, "models_rank.go": true},
		// The other readers serve only the disclosure verb's other sources
		// (--source scorecard, --db), which --models never reads.
		"scorecardRankEvidence(": {"models_rank.go": true},
		"bugcatchRankEvidence(":  {"models_rank.go": true},
		"warehouseRankEvidence(": {"models_rank.go": true},
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f) // #nosec G304 -- this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		for imp, ok := range allowed {
			if strings.Contains(string(b), imp) && !ok[f] {
				t.Errorf("%s uses %s; only %v may", f, imp, allowedFiles(ok))
			}
		}
	}
}

func allowedFiles(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
