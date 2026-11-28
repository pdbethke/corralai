// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"github.com/pdbethke/corralai/internal/lang"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// THE SAFETY ARGUMENT FOR A SHARED GRADING CACHE, run against the real jail.
//
// go keeps test RESULTS in the build cache and replays one ("(cached)") rather
// than run the test again. A cache shared among graded runs would therefore be
// able to answer a mutant's run with the unmutated code's result: a kill
// reported as a pass, or the other way round. Three facts keep that from
// happening, and this test runs each of them:
//
//  1. the warming run forbids a cached result (-count=1), so it stores none;
//  2. frozen runs cannot store one for each other, because their writes vanish
//     with the run (the jail's copy-on-write overlay);
//  3. a changed source file is always recompiled, so the changed behaviour is
//     observed, never an old result.
//
// Measured while writing it: with go's default flags a result is NOT replayed
// from one fresh workspace to another, because the test binary embeds the
// workspace's temp path and so differs run to run. A replay needs a build whose
// binary does not (-trimpath), which is why the sequence is run twice, plain
// and with -trimpath, and why the negative control (a warming run that does
// NOT forbid a cached result) is required to show a replay for -trimpath. A
// green result for that variant is therefore not a cache that was never
// shared; the plain variant is what every graded run sends today.
func TestAWarmRunThatForbidsACachedResultLeavesNoneToReplay(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	backend := overlayJailOrSkip(t)
	t.Setenv("TMPDIR", t.TempDir())
	newJail := func() RunCacheJail {
		j := NewJail(backend, 2*time.Minute, WithRunCache(&lang.EvidenceCache{Env: "GOCACHE"})).(RunCacheJail)
		t.Cleanup(func() { _ = j.Close() })
		if c := j.RunCache(); c.Target == "" {
			t.Skipf("no shared cache on this host (%s)", c.Unshared)
		}
		return j
	}
	files := func(ret string) map[string]string {
		return map[string]string{
			"go.mod": "module example.com/m\n\ngo 1.21\n",
			"p/p.go": "package p\n\nfunc One() int { return " + ret + " }\n",
			"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n" +
				"\tif One() != 1 {\n\t\tt.Fatal(\"One() is not 1\")\n\t}\n}\n",
		}
	}
	ctx := context.Background()
	run := func(j RunCacheJail, f map[string]string, cmd []string) (bool, string) {
		t.Helper()
		ok, out, err := j.(bwrapJail).RunTestVerbose(ctx, f, cmd)
		if err != nil {
			t.Fatalf("run %v: %v", cmd, err)
		}
		return ok, out
	}

	for _, v := range []struct {
		name  string
		flags []string
	}{{"plain", nil}, {"trimpath", []string{"-trimpath"}}} {
		t.Run(v.name, func(t *testing.T) {
			cmd := append([]string{"go", "test"}, append(v.flags, "./...")...)
			warm := append(append([]string{}, cmd...), "-count=1")

			if v.name == "trimpath" {
				// The negative control: a warming run that stores a result
				// leaves one for a frozen run to replay.
				control := newJail()
				if ok, out, err := control.WarmRun(ctx, files("1"), cmd); err != nil || !ok {
					t.Fatalf("control warm run: ok=%v err=%v\n%s", ok, err, out)
				}
				if _, out := run(control, files("1"), cmd); !strings.Contains(out, "(cached)") {
					t.Fatalf("negative control: a warming run that allows a cached result should leave one for a frozen run to replay, got:\n%s\nso this test could not see a replay", out)
				}
			}

			j := newJail()
			if ok, out, err := j.WarmRun(ctx, files("1"), warm); err != nil || !ok {
				t.Fatalf("warm run: ok=%v err=%v\n%s", ok, err, out)
			}
			for i := 1; i <= 2; i++ {
				ok, out := run(j, files("1"), cmd)
				if !ok || strings.Contains(out, "(cached)") {
					t.Errorf("frozen run %d on the unmutated tree: ok=%v, and no output line may say (cached):\n%s", i, ok, out)
				}
			}
			// One changed byte: recompiled, and the failing test is seen.
			ok, out := run(j, files("2"), cmd)
			if ok || strings.Contains(out, "(cached)") || !strings.Contains(out, "One() is not 1") {
				t.Errorf("frozen run on a changed file: ok=%v; it must fail with the test's own message and replay nothing:\n%s", ok, out)
			}
		})
	}
}
