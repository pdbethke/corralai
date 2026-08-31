//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"os"
	"testing"
)

// TestPHPSignatures reads the real-file fixture (its parse tree is dumped
// alongside at testdata/php/sample.tree.txt — the extractor below is written
// against those OBSERVED node names, never guessed ones) and checks the
// callable surface: a namespaced class's methods (Receiver = the class), a
// trait method (Receiver = the trait), a top-level function, and that a
// bodiless interface member is never extracted.
func TestPHPSignatures(t *testing.T) {
	src, err := os.ReadFile("testdata/php/sample.php")
	if err != nil {
		t.Fatal(err)
	}
	sigs, err := ExtractSignatures(string(src), "php")
	if err != nil {
		t.Fatalf("ExtractSignatures(php): %v", err)
	}
	got := idsOf(sigs)

	for _, want := range []string{
		"Invoice.__construct",
		"Invoice.price",
		"Invoice.currency",
		"Invoice.describe",
		"Invoice.status",
		"Loggable.log",
		"formatTotal",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOfSigs(got))
		}
	}

	// Interface members have no body — never extracted (iron rule: no
	// bodiless declarations).
	for _, unwanted := range []string{"Priceable.price", "Priceable.currency"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("bodiless interface member %q was extracted, want skipped", unwanted)
		}
	}

	if s := got["Invoice.price"]; s.Kind != "method" || s.Receiver != "Invoice" {
		t.Errorf("Invoice.price = %+v, want method with Receiver=Invoice", s)
	}
	if s := got["formatTotal"]; s.Receiver != "" || s.Kind != "func" {
		t.Errorf("formatTotal = %+v, want a free function with no receiver", s)
	}
	// A branching method must not score the straight-line floor.
	if s := got["Invoice.price"]; s.Complexity <= 1 {
		t.Errorf("Invoice.price complexity = %d, want > 1 — an if is not straight-line", s.Complexity)
	}
	// match: each match_conditional_expression arm is a decision point; the
	// default arm is the fall-through and does not add one (mirrors how the
	// JS extractor treats switch_default).
	if s := got["Invoice.describe"]; s.Complexity != 3 {
		t.Errorf("Invoice.describe complexity = %d, want 3 (1 + 2 match arms, default excluded)", s.Complexity)
	}
	// switch: each case_statement arm is a decision point (case 2/case 3
	// fall-through is still two arms); the default_statement does not add one.
	if s := got["Invoice.status"]; s.Complexity != 4 {
		t.Errorf("Invoice.status complexity = %d, want 4 (1 + 3 case arms, default excluded)", s.Complexity)
	}
}

// TestPHPSignaturesDistinctReceiverIdentity is the review rider from Task 1:
// two different classes defining a method of the SAME name must not collapse
// to one symbol identity (Receiver + "." + Name) — advpool.symbolIdentity
// relies on this to bin-pack shards without one class's method silently
// standing in for the other's.
func TestPHPSignaturesDistinctReceiverIdentity(t *testing.T) {
	const src = `<?php
class Invoice {
    public function handle(): void
    {
        echo "invoice";
    }
}

class Refund {
    public function handle(): void
    {
        echo "refund";
    }
}
`
	sigs, err := ExtractSignatures(src, "php")
	if err != nil {
		t.Fatalf("ExtractSignatures(php): %v", err)
	}
	got := idsOf(sigs)
	for _, want := range []string{"Invoice.handle", "Refund.handle"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOfSigs(got))
		}
	}
	if len(sigs) != 2 {
		t.Fatalf("got %d signatures, want exactly 2 (one per class) — a collapse would report fewer: %+v", len(sigs), sigs)
	}
	if got["Invoice.handle"].Receiver != "Invoice" || got["Refund.handle"].Receiver != "Refund" {
		t.Errorf("Receiver did not disambiguate the two classes' same-named method: %+v", got)
	}
}
