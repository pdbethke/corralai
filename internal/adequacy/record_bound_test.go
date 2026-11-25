// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// recordMarkers bounds the bytes the record wrapper prints around a record
// it prints: a blank line, `corral-record-begin <format>`, a newline it may
// supply, and `corral-record-end` (lang.RecordPrintMax). The longest format
// word is nine bytes, so the block's own lines are under 64 bytes; 256 is a
// stated allowance, not a measurement.
const recordMarkers = 256

// The record a hooked run prints is bounded so that it never takes the
// printed failure lines' room in the tail adequacy keeps of a mutant's run
// (maxDetailedOutput): a printed record and its markers leave at least half
// the tail to what the runner printed.
func TestRecordPrintMaxLeavesTheTailRoom(t *testing.T) {
	headroom := maxDetailedOutput / 2
	if lang.RecordPrintMax+recordMarkers+headroom > maxDetailedOutput {
		t.Errorf("lang.RecordPrintMax %d + markers %d + headroom %d > the kept tail %d: a printed record leaves the printed lines less than half the tail",
			lang.RecordPrintMax, recordMarkers, headroom, maxDetailedOutput)
	}
}

// recordBoundCase is one hooked row, run through its real hook with a
// stand-in for the runner (fakeHookedRunner) that prints what the real
// runner printed in a capture and writes that capture's record, padded or
// not, where the hook told it to.
type recordBoundCase struct {
	lang, capture string
	argv          []string
	// same reports whether a name the hooked run gives is one the printed
	// lines alone give: the same test, written as the printed lines write
	// it. nil is equality.
	same func(hooked, printed string) bool
	// firstMayDiffer is for a row whose first named test differs with the
	// record by design (swift: the record names a swift-testing failure,
	// the printed lines an XCTest one first).
	firstMayDiffer bool
}

var recordBoundCases = []recordBoundCase{
	{lang: "php", capture: "phpunit-hook-fail.txt", argv: []string{"phpunit", "tests"}},
	{lang: "javascript", capture: "jest-hook-fail.txt", argv: []string{"jest"},
		// the record keeps a title holding ` › ` whole; the printed
		// heading splits it.
		same: func(h, p string) bool { return h == p || strings.ReplaceAll(h, " › ", "::") == p }},
	{lang: "ruby", capture: "rspec-hook-fail.txt", argv: []string{"rspec"}},
	{lang: "rust", capture: "nextest-hook-fail.txt", argv: []string{"cargo", "nextest", "run"}},
	{lang: "javascript", capture: "node-hook-fail.txt", argv: []string{"node", "--test"}},
	{lang: "swift", capture: "swift-hook-fail-6.4.2.txt", argv: []string{"swift", "test"}, firstMayDiffer: true},
}

// fakeHookedRunner stands in for each hooked runner under its own name: it
// prints the file $FAKE_PRINTED, writes the file $FAKE_RECORD where the
// hook's words say the runner writes its record, and exits 1. The
// destinations are the six rows' (PHPUnit's --log-junit, rspec's --out,
// jest's --outputFile=, node's second --test-reporter-destination=,
// swift-testing's --xunit-output=, and the junit path in the config
// cargo-nextest is handed with --tool-config-file corral:<file>).
const fakeHookedRunner = `#!/bin/sh
cat "$FAKE_PRINTED"
dest= next=
for a do
  if [ -n "$next" ]; then dest=$a; next=; continue; fi
  case $a in
    --log-junit|--out) next=1 ;;
    --outputFile=*) dest=${a#--outputFile=} ;;
    --test-reporter-destination=stdout) ;;
    --test-reporter-destination=*) dest=${a#--test-reporter-destination=} ;;
    --xunit-output=*) dest=${a#--xunit-output=} ;;
    corral:*) dest=$(sed -n "s/^path = '\(.*\)'$/\1/p" "${a#corral:}") ;;
  esac
done
[ -n "$dest" ] && cat "$FAKE_RECORD" > "$dest"
exit 1
`

// runHooked runs argv as the row's hook grades it (lang.ReportCmd), the
// runner being fakeHookedRunner, and returns what adequacy keeps of it on
// the mutant path: its last maxDetailedOutput bytes.
func runHooked(t *testing.T, c recordBoundCase, printed, record string) string {
	t.Helper()
	p, ok := lang.ByName(c.lang)
	if !ok {
		t.Fatalf("no plugin %q", c.lang)
	}
	cmd, applied := lang.ReportCmd(p, lang.Cmd(c.argv), c.argv)
	if !applied {
		t.Fatalf("ReportCmd(%v) did not apply the row's hook", c.argv)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, c.argv[0]), []byte(fakeHookedRunner), 0o700); err != nil { // #nosec G306 -- an executable the test runs
		t.Fatal(err)
	}
	pf, rf := filepath.Join(dir, "printed"), filepath.Join(dir, "record")
	for f, s := range map[string]string{pf: printed, rf: record} {
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the row's own wrapper around a test's stand-in runner
	run.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_PRINTED="+pf, "FAKE_RECORD="+rf, "NODE_OPTIONS=")
	out, err := run.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("%v: %v, want the stand-in's exit 1:\n%s", cmd, err, out)
	}
	return string(tailBytes(out, maxDetailedOutput))
}

// failingNames is every test the row names failing in out: its salvage
// list (lang.DeselectorFor), or its first failure where it has none
// (swift).
func failingNames(t *testing.T, c recordBoundCase, out string) []string {
	t.Helper()
	p, _ := lang.ByName(c.lang)
	if failed, ok := lang.DeselectorFor(p, lang.Cmd(c.argv)); ok {
		return failed(out)
	}
	fp := lang.FailureParserFor(p, lang.Cmd(c.argv))
	if fp == nil {
		t.Fatalf("%v: no failure reader", c.argv)
	}
	if f := fp.FirstFailure([]byte(out)); f != "" {
		return []string{f}
	}
	return nil
}

// padRecord is record with passing tests added, in its own format, until it
// is more than size bytes.
func padRecord(t *testing.T, format, record string, size int) string {
	t.Helper()
	for i := 0; len(record) <= size; i++ {
		switch format {
		case "junit":
			at := strings.LastIndex(record, "</testsuite>")
			if at < 0 {
				t.Fatalf("no </testsuite> in the record")
			}
			pad := fmt.Sprintf(`<testcase name="padding_passes_%d" classname="padding"/>`+"\n", i)
			record = record[:at] + pad + record[at:]
		case "jest-json", "rspec-json":
			var v map[string]any
			if err := json.Unmarshal([]byte(record), &v); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 50; n++ {
				name := fmt.Sprintf("padding passes %d.%d", i, n)
				if format == "jest-json" {
					file := v["testResults"].([]any)[0].(map[string]any)
					file["assertionResults"] = append(file["assertionResults"].([]any),
						map[string]any{"ancestorTitles": []any{"padding"}, "title": name, "status": "passed", "failureMessages": []any{}})
				} else {
					v["examples"] = append(v["examples"].([]any), map[string]any{"full_description": name, "status": "passed"})
				}
			}
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			record = string(b)
		default:
			t.Fatalf("no padding for format %q", format)
		}
	}
	return record
}

// A record over lang.RecordPrintMax costs the run none of its printed
// failure lines: padded with passing tests past the bound and past the
// whole tail adequacy keeps (where, printed, it would push every printed
// line out and lose its own beginning, so the block is incomplete and the
// hooked run would name nothing), the hooked run's kept tail names every
// test the printed lines alone name. Under the bound (the capture's own
// record) the hooked run names the same tests, the record's form of them,
// except where the row's first failure changes with the record by design.
//
// "The printed lines alone" are the capture's printed lines
// (lang.PrintedOutput of the hooked capture): each row's hook-capture tests
// in internal/lang pin them against the same run without the hook.
func TestARecordOverTheBoundCostsNoPrintedFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to run the record wrapper")
	}
	for _, c := range recordBoundCases {
		t.Run(c.capture, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "lang", "testdata", "runners", c.capture))
			if err != nil {
				t.Fatal(err)
			}
			capture := string(raw)
			format, record, ok := lang.RecordBlock(capture)
			if !ok {
				t.Fatalf("%s holds no record block", c.capture)
			}
			printed := lang.PrintedOutput(capture)
			unhooked := failingNames(t, c, string(tailBytes([]byte(printed), maxDetailedOutput)))
			if len(unhooked) == 0 {
				t.Fatalf("the printed lines alone name no test; the case shows nothing")
			}
			same := c.same
			if same == nil {
				same = func(h, p string) bool { return h == p }
			}

			t.Run("over the bound", func(t *testing.T) {
				// past the whole kept tail, and so past the bound, which
				// is under it (TestRecordPrintMaxLeavesTheTailRoom).
				padded := padRecord(t, format, record, 2*maxDetailedOutput)
				out := runHooked(t, c, printed, padded)
				if _, _, ok := lang.RecordBlock(out); ok {
					t.Errorf("a %d-byte record, over the %d-byte bound, was printed", len(padded), lang.RecordPrintMax)
				}
				want := fmt.Sprintf("corral-record-skipped %s %d\n", format, len(padded))
				if !strings.HasSuffix(out, want) {
					t.Errorf("the kept tail does not end with %q", want)
				}
				hooked := failingNames(t, c, out)
				for _, u := range unhooked {
					if !slices.Contains(hooked, u) {
						t.Errorf("the hooked run names %q, without %q, which the printed lines alone name (%q)", hooked, u, unhooked)
					}
				}
			})

			t.Run("under the bound", func(t *testing.T) {
				if len(record) > lang.RecordPrintMax {
					t.Fatalf("the capture's record is %d bytes, over the bound", len(record))
				}
				out := runHooked(t, c, printed, record)
				if _, _, ok := lang.RecordBlock(out); !ok {
					t.Fatalf("a %d-byte record, under the bound, was not printed", len(record))
				}
				hooked := failingNames(t, c, out)
				if c.firstMayDiffer {
					if len(hooked) == 0 {
						t.Errorf("the hooked run names nothing; the printed lines alone name %q", unhooked)
					}
					return
				}
				if len(hooked) != len(unhooked) {
					t.Errorf("the hooked run names %q, the printed lines alone %q: not the same tests", hooked, unhooked)
				}
				for _, u := range unhooked {
					if !slices.ContainsFunc(hooked, func(h string) bool { return same(h, u) }) {
						t.Errorf("the hooked run names %q, without %q, which the printed lines alone name", hooked, u)
					}
				}
			})
		})
	}
}

// THE RESIDUAL, pinned so that a fix flips it deliberately. A record
// printed under lang.RecordPrintMax still takes its own size out of the
// tail adequacy keeps, so the printed lines keep only what is left (at
// least half the tail). A failure that only the printed lines name can then
// fall out of the kept tail when the printed output is longer than that
// remainder. PHPUnit's throwing setUpBeforeClass and throwing data provider
// are errors of that kind: the JUnit record leaves them out, and PHPUnit
// prints them in its errors section, which comes before the failures. In
// the edge capture, with 40 KiB of printed failure text and the record
// padded to just under the bound, the run without the hook names both and
// the hooked run names neither. Swift XCTest failures and the tests of a
// crashed swift-testing process are the same kind. A capture that keeps the
// record apart from the printed tail (the queued follow-up) would make the
// hooked run name them, and this test then fails and must be inverted.
func TestARecordUnderTheBoundCanCostPrintedOnlyFailures(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to run the record wrapper")
	}
	c := recordBoundCase{lang: "php", capture: "phpunit-hook-edge.txt", argv: []string{"phpunit", "tests"}}
	raw, err := os.ReadFile(filepath.Join("..", "lang", "testdata", "runners", c.capture))
	if err != nil {
		t.Fatal(err)
	}
	format, record, ok := lang.RecordBlock(string(raw))
	if !ok {
		t.Fatal("no record block in the edge capture")
	}
	printed := lang.PrintedOutput(string(raw))
	filler := strings.Repeat("     | some long assertion diff line, printed by PHPUnit for a failure\n", 600)
	if !strings.Contains(printed, "There were 2 failures:") {
		t.Fatal("the edge capture no longer has the failures heading the filler goes after")
	}
	printed = strings.Replace(printed, "There were 2 failures:", "There were 2 failures:\n"+filler, 1)
	padded := padRecord(t, format, record, 29<<10)
	if len(padded) > lang.RecordPrintMax {
		t.Fatalf("padded record %d bytes, over the bound", len(padded))
	}

	unhooked := failingNames(t, c, string(tailBytes([]byte(printed), maxDetailedOutput)))
	out := runHooked(t, c, printed, padded)
	if _, _, ok := lang.RecordBlock(out); !ok {
		t.Fatal("the record, under the bound, was not printed")
	}
	hooked := failingNames(t, c, out)
	for _, lost := range []string{"ProviderTest::testP", "SetupTest"} {
		if !slices.Contains(unhooked, lost) {
			t.Fatalf("the run without the hook does not name %q (%q); the case shows nothing", lost, unhooked)
		}
		if slices.Contains(hooked, lost) {
			t.Errorf("the hooked run names %q: the documented residual is gone; invert this test and correct the disclosures (RecordPrintMax, the hooked rows' Notes, the CHANGELOG)", lost)
		}
	}
}
