// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

// verdictShape renders advpool.Verdict's serialized shape as the string this
// gate hashes: one line per exported field — name, type, json tag — sorted by
// name so a reorder is not a change.
func verdictShape() (string, int) {
	ty := reflect.TypeOf(advpool.Verdict{})
	parts := make([]string, 0, ty.NumField())
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		if f.PkgPath != "" {
			continue // unexported: never serialized, never read back
		}
		parts = append(parts, f.Name+" "+f.Type.String()+" "+f.Tag.Get("json"))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n"), len(parts)
}

// TestVerdictShapeIsPinnedToItsGeneration is the gate on a HAND-MAINTAINED
// value that went ten commits without being maintained.
//
// THE DEFECT IT EXISTS TO PREVENT. VerdictGeneration is the EngineVersion
// component of the verdict cache key. A cached verdict is stored as
// verdict_json and read back with encoding/json, which unmarshals an OLDER
// document cleanly and leaves every field added since at its zero value — no
// error, no warning. So a Verdict that gains a field while the generation
// stays put can serve a stale document as a current one, and it is then
// re-signed: a claim about behaviour that was never measured, written into a
// record that is tamper-evident and therefore uncorrectable afterwards.
//
// That is not hypothetical. Between generations "4" and "5" the struct gained
// thirteen fields and the generation did not move; a code review found it,
// which is precisely the failure mode this repository keeps naming — a real
// value with nothing mechanical watching it.
//
// HOW TO FIX A FAILURE HERE, in order:
//
//  1. Decide whether your change can move a verdict for unchanged source. Read
//     VerdictGeneration's doc comment; it says "when unsure, bump."
//  2. Bump VerdictGeneration and add a line to its history.
//  3. Only then paste the new hash below into VerdictShapeSHA256.
//
// Doing (3) alone makes this test green and defeats it entirely. The hash is
// not the point; the constant sitting next to the generation is.
func TestVerdictShapeIsPinnedToItsGeneration(t *testing.T) {
	shape, n := verdictShape()
	sum := sha256.Sum256([]byte(shape))
	got := hex.EncodeToString(sum[:])

	if got != VerdictShapeSHA256 {
		t.Errorf("advpool.Verdict's serialized shape changed (%d exported fields).\n"+
			"  pinned: %s\n"+
			"  actual: %s\n\n"+
			"A cached verdict is read back with encoding/json, which fills every field added since\n"+
			"at its zero value in silence — so a shape change with an unchanged VerdictGeneration\n"+
			"lets a stale document be served, and re-signed, as a current measurement.\n\n"+
			"Do NOT just paste the new hash. First decide whether VerdictGeneration must be bumped\n"+
			"(its doc comment says: when unsure, bump), bump it and add a history line, and only\n"+
			"then update VerdictShapeSHA256. Current generation is %q.\n\n"+
			"shape:\n%s",
			n, VerdictShapeSHA256, got, VerdictGeneration, shape)
	}

	// A reflection walk that found nothing would pass green forever.
	if n == 0 {
		t.Fatal("read zero exported fields from advpool.Verdict — this gate is not looking where it thinks it is")
	}
}

// TestVerdictShapeIgnoresFieldOrder pins the one change this gate deliberately
// does NOT report: field order is invisible to encoding/json, so reordering
// must not cost a generation bump (and the recompute bill that comes with it).
func TestVerdictShapeIgnoresFieldOrder(t *testing.T) {
	shape, _ := verdictShape()
	lines := strings.Split(shape, "\n")
	if len(lines) < 2 {
		t.Skip("too few fields to test ordering")
	}
	if !sort.StringsAreSorted(lines) {
		t.Errorf("verdictShape did not sort its lines, so a pure field reorder would read as a shape change and force an unnecessary bump")
	}
}
