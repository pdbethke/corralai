// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/pdbethke/corralai/internal/scanstore"
)

// scansShowJSONFixture is the one-file, two-model-call fixture both tests
// below share: a golden test for `--json --timing` and a byte-compare test
// proving `--json` alone is untouched need to agree on what the ledger holds,
// or a divergence in the fixture itself could hide a real regression.
func scansShowJSONFixture() *fakeScansReader {
	retries := 1
	cachedTokens := int64(240000)
	cacheWrites := int64(38000)
	return &fakeScansReader{
		files: []scanstore.File{{
			Path: "pkg/a.py", Lang: "python", Disposition: "audited", Gradable: true,
			KillRate: ptrF(0.72), Survivors: 3, ProvenMissed: 1,
		}},
		scan: scanstore.ScanRow{ID: 1, Scan: scanstore.Scan{SelectionMillis: ms(92000)}},
		modelCalls: []scanstore.ModelCall{
			{ScanID: 1, Path: "pkg/a.py", Role: "mutant-generator", Model: "m-1",
				Calls: 24, Retries: &retries, InputTokens: 900000, OutputTokens: 31000, WallMillis: 4100},
			// The writer seat is the one that sends a byte-identical prefix on
			// every call of a file, so it is the seat a caching provider
			// actually reports on; the generator row leaves it NULL.
			{ScanID: 1, Path: "pkg/a.py", Role: "test-writer", Model: "w-1",
				Calls: 5, InputTokens: 300000, OutputTokens: 17000,
				CachedInputTokens: &cachedTokens, CacheWriteInputTokens: &cacheWrites,
				WallMillis: 2200},
		},
	}
}

// wantScansShowJSONWithTiming is the golden `--json --timing` document: the
// file array under "files", the scan-grain selection_ms this ledger reads
// nowhere else, and one model_calls row per scan_model_calls row —
// snake_case keys, NULLs where the ledger genuinely has none (retries on the
// second call, cached_input_tokens on the generator — which reported none),
// never a stored 0 standing in for "not measured".
const wantScansShowJSONWithTiming = `{
  "files": [
    {
      "Path": "pkg/a.py",
      "Lang": "python",
      "Disposition": "audited",
      "Reason": "",
      "KillRate": 0.72,
      "Survivors": 3,
      "Gradable": true,
      "PreflightState": "",
      "Evidence": "",
      "Detail": "",
      "TimedOut": false,
      "TestWriterFailed": false,
      "PoolTestUnsound": false,
      "ProvenMissed": 1,
      "ProvenMutantIDs": "",
      "AuthoredTest": "",
      "TestSelection": "",
      "SelectedTests": 0,
      "SuiteTests": 0,
      "SelectionFallback": "",
      "WriterMode": "",
      "Uncovered": false,
      "ImportOnly": null,
      "CoveringTests": null,
      "MutantsFrom": "",
      "Trees": 0,
      "ConcurrencyNote": "",
      "SharedDirs": "",
      "CacheKey": "",
      "VerdictJSON": "",
      "ComputedAt": "0001-01-01T00:00:00Z",
      "ModelsByRole": "",
      "MutantsTotal": 0,
      "RegionsTotal": 0,
      "RegionsProbed": 0,
      "DroppedRegions": "",
      "VacuousFindings": 0,
      "Status": "",
      "PromptShape": "",
      "MutantBudget": null,
      "MutantBudgetRule": "",
      "Complexity": null,
      "Symbols": null,
      "SymbolsProbed": null,
      "Decisions": null,
      "DecisionsProbed": null,
      "AuthoredTestNotCollected": false,
      "BaselineFailed": false,
      "SuiteBaselineMillis": 0,
      "CacheHit": false,
      "ReusedFromScanID": null,
      "ParentSHA256": "",
      "MutantsGraded": 0,
      "MutantsInvalid": 0,
      "MutantsTimedOut": null,
      "SelectionMillis": null,
      "GenerationMillis": null,
      "PoolMillis": null,
      "DevPassMillis": null,
      "AuthoredPassMillis": null,
      "CriticMillis": null,
      "TotalMillis": null,
      "MutantMillisMedian": null,
      "MutantMillisMax": null,
      "ChallengerJaccard": null,
      "ChallengerKappa": null,
      "ChallengerSufficient": null,
      "ChallengerMutants": null,
      "ChallengerSurvivedWriter": null,
      "ChallengerSurvivedShadow": null,
      "ChallengerUnion": null,
      "ChallengerShared": null,
      "GoalsDerived": 0,
      "GoalReused": null,
      "PerMutant": false,
      "TestsPerMutantMin": null,
      "TestsPerMutantMedian": null,
      "TestsPerMutantMax": null
    }
  ],
  "selection_ms": 92000,
  "selection_reused_from": null,
  "model_calls": [
    {
      "path": "pkg/a.py",
      "role": "mutant-generator",
      "model": "m-1",
      "calls": 24,
      "retries": 1,
      "input_tokens": 900000,
      "output_tokens": 31000,
      "cached_input_tokens": null,
      "cache_write_input_tokens": null,
      "wall_ms": 4100
    },
    {
      "path": "pkg/a.py",
      "role": "test-writer",
      "model": "w-1",
      "calls": 5,
      "retries": null,
      "input_tokens": 300000,
      "output_tokens": 17000,
      "cached_input_tokens": 240000,
      "cache_write_input_tokens": 38000,
      "wall_ms": 2200
    }
  ]
}
`

// TestScansShowJSONWithTimingCarriesTheSelectionAndModelCalls is the golden
// test for the new shape: it exists because the text `--timing` readout
// already prints scan_model_calls rows and the scan-grain selection_ms, and
// `--json` had no way to carry either — forcing a docs fixture to
// hand-transcribe cost numbers out of text instead of reading them back.
func TestScansShowJSONWithTimingCarriesTheSelectionAndModelCalls(t *testing.T) {
	r := scansShowJSONFixture()
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--json", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if out.String() != wantScansShowJSONWithTiming {
		t.Errorf("`scans show --json --timing` =\n%s\nwant:\n%s", out.String(), wantScansShowJSONWithTiming)
	}
}

// wantScansShowJSONBare is `--json` alone over the SAME fixture: the bare
// file array, unwrapped, exactly what this command has always printed.
const wantScansShowJSONBare = `[
  {
    "Path": "pkg/a.py",
    "Lang": "python",
    "Disposition": "audited",
    "Reason": "",
    "KillRate": 0.72,
    "Survivors": 3,
    "Gradable": true,
    "PreflightState": "",
    "Evidence": "",
    "Detail": "",
    "TimedOut": false,
    "TestWriterFailed": false,
    "PoolTestUnsound": false,
    "ProvenMissed": 1,
    "ProvenMutantIDs": "",
    "AuthoredTest": "",
    "TestSelection": "",
    "SelectedTests": 0,
    "SuiteTests": 0,
    "SelectionFallback": "",
    "WriterMode": "",
    "Uncovered": false,
    "ImportOnly": null,
    "CoveringTests": null,
    "MutantsFrom": "",
    "Trees": 0,
    "ConcurrencyNote": "",
    "SharedDirs": "",
    "CacheKey": "",
    "VerdictJSON": "",
    "ComputedAt": "0001-01-01T00:00:00Z",
    "ModelsByRole": "",
    "MutantsTotal": 0,
    "RegionsTotal": 0,
    "RegionsProbed": 0,
    "DroppedRegions": "",
    "VacuousFindings": 0,
    "Status": "",
    "PromptShape": "",
    "MutantBudget": null,
    "MutantBudgetRule": "",
    "Complexity": null,
    "Symbols": null,
    "SymbolsProbed": null,
    "Decisions": null,
    "DecisionsProbed": null,
    "AuthoredTestNotCollected": false,
    "BaselineFailed": false,
    "SuiteBaselineMillis": 0,
    "CacheHit": false,
    "ReusedFromScanID": null,
    "ParentSHA256": "",
    "MutantsGraded": 0,
    "MutantsInvalid": 0,
    "MutantsTimedOut": null,
    "SelectionMillis": null,
    "GenerationMillis": null,
    "PoolMillis": null,
    "DevPassMillis": null,
    "AuthoredPassMillis": null,
    "CriticMillis": null,
    "TotalMillis": null,
    "MutantMillisMedian": null,
    "MutantMillisMax": null,
    "ChallengerJaccard": null,
    "ChallengerKappa": null,
    "ChallengerSufficient": null,
    "ChallengerMutants": null,
    "ChallengerSurvivedWriter": null,
    "ChallengerSurvivedShadow": null,
    "ChallengerUnion": null,
    "ChallengerShared": null,
    "GoalsDerived": 0,
    "GoalReused": null,
    "PerMutant": false,
    "TestsPerMutantMin": null,
    "TestsPerMutantMedian": null,
    "TestsPerMutantMax": null
  }
]
`

// TestScansShowJSONWithoutTimingIsByteIdenticalToTheOldShape pins the OTHER
// half of the contract: --json without --timing must be indistinguishable
// from before this change, byte for byte, over the very fixture that now
// carries model-call rows and a selection_ms — proving the wrap only ever
// happens when --timing asks for it.
func TestScansShowJSONWithoutTimingIsByteIdenticalToTheOldShape(t *testing.T) {
	r := scansShowJSONFixture()
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--json"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if out.String() != wantScansShowJSONBare {
		t.Errorf("`scans show --json` (no --timing) =\n%s\nwant (unchanged, pre-existing shape):\n%s", out.String(), wantScansShowJSONBare)
	}
}

// TestScansShowJSONWithTimingCarriesSelectionReusedFrom is IMPORTANT-5's
// headline case: a scan that reused a prior scan's selection evidence (so
// SelectionMS is nil, per scanstore.Scan.SelectionReusedFrom's own doc)
// must carry that scan's id under "selection_reused_from", not a null the
// text readout would have disclosed but --json --timing dropped.
func TestScansShowJSONWithTimingCarriesSelectionReusedFrom(t *testing.T) {
	r := scansShowJSONFixture()
	reusedFrom := int64(7)
	r.scan.SelectionMillis = nil
	r.scan.SelectionReusedFrom = &reusedFrom

	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--json", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	var decoded scansShowJSON
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if decoded.SelectionMS != nil {
		t.Errorf("selection_ms = %v, want nil — the pass did not run this scan", *decoded.SelectionMS)
	}
	if decoded.SelectionReusedFrom == nil || *decoded.SelectionReusedFrom != 7 {
		t.Errorf("selection_reused_from = %v, want *7", decoded.SelectionReusedFrom)
	}
}
