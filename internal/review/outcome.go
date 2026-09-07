// SPDX-License-Identifier: Elastic-2.0

package review

// Outcome is what the record finally says about one finding, for grading
// the seats that argued over it. The order of authority is the design's:
// a person's adjudication when there is one; else execution — the
// finding's recorded tier after the reviewer's script ran and after any
// reproduced refutation demoted it. A finding nobody executed and nobody
// adjudicated (a CODE-READ or HYPOTHESIS claim with no verdict) has NO
// outcome, and grades nobody: not finding it false is not the same as it
// being true.
type Outcome struct {
	Known bool // an outcome exists
	Held  bool // the claim held
	By    string
}

// Adjudicated is the one thing the outcome rule needs from outside the
// package: a person's verdict on this finding, if any ("confirmed" or
// "refuted"), and who gave it.
type Adjudicated struct {
	Verdict string
	By      string
}

// OutcomeOf applies the rule.
func OutcomeOf(f Finding, adj *Adjudicated) Outcome {
	if adj != nil {
		switch adj.Verdict {
		case "confirmed":
			return Outcome{Known: true, Held: true, By: "adjudication by " + adj.By}
		case "refuted":
			return Outcome{Known: true, Held: false, By: "adjudication by " + adj.By}
		}
	}
	// Execution. A refutation that REPRODUCED — its script ran and exited
	// 0 — is an outcome for a finding of ANY tier: the claim fell. (It was
	// run and recorded for every tier but only consulted for REPRODUCED
	// claims: review 61dc210a39fd#R2.)
	if x := f.Refutation; x != nil && x.Verdict == VerdictRefuted && x.Tier == TierReproduced && x.ExitCode != nil && *x.ExitCode == 0 {
		return Outcome{Known: true, Held: false, By: "execution (refutation by " + x.Model + ")"}
	}
	// A claim declared REPRODUCED was put to the tree — unless the harness
	// could not run it, in which case nothing was, and there is no outcome
	// (review 61dc210a39fd#R1).
	if f.Declared == TierReproduced && f.Unrun == "" {
		// HELD by execution means a script ran and exited 0 — on the record,
		// as ExitCode. A finding still marked REPRODUCED with no exit code
		// never ran (an entry written by hand, or an older writer) and is no
		// outcome, not a credit (review a906b2676dca#R1, Codex reviewing,
		// Gemini verifying).
		if f.Tier == TierReproduced {
			if f.ExitCode != nil && *f.ExitCode == 0 {
				return Outcome{Known: true, Held: true, By: "execution"}
			}
			return Outcome{}
		}
		return Outcome{Known: true, Held: false, By: "execution"}
	}
	return Outcome{}
}

// Graded is what one finding contributes to the two seats' rows.
type Graded struct {
	ReviewerChecked, ReviewerHeld   bool
	VerifierCalled, VerifierCorrect bool
}

// Grade scores one finding for both seats. The reviewer is graded on the
// outcome; the verifier on whether its verdict agreed with it — REFUTED
// against a claim that did not hold, STANDS against one that did. A
// verifier that gave no verdict on the finding is not graded on it.
func Grade(f Finding, adj *Adjudicated) Graded {
	o := OutcomeOf(f, adj)
	if !o.Known {
		return Graded{}
	}
	g := Graded{ReviewerChecked: true, ReviewerHeld: o.Held}
	if x := f.Refutation; x != nil {
		g.VerifierCalled = true
		switch x.Verdict {
		case VerdictRefuted:
			g.VerifierCorrect = !o.Held
		default:
			g.VerifierCorrect = o.Held
		}
	}
	return g
}
