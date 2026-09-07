// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Refutation is the verifier's answer to one finding. Declared is the tier
// the verifier asserted for its refutation; Tier is what the record
// carries after its script ran — the same demote-only rule the reviewer's
// findings live under. Verdict is VerdictRefuted or VerdictStands.
type Refutation struct {
	Model    string `json:"model"`
	Verdict  string `json:"verdict"`
	Argument string `json:"argument"`
	Declared string `json:"declared,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Script   string `json:"script,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Demoted  string `json:"demoted,omitempty"`
}

// The verifier's two verdicts.
const (
	VerdictRefuted = "REFUTED"
	VerdictStands  = "STANDS"
)

// VerifierBriefSystem is the verifier seat's standing instructions: refute
// the reviewer, by the same rules, and never mistake not finding something
// for having shown it absent.
const VerifierBriefSystem = `You are a VERIFIER. Another model reviewed a scope of a repository you have never seen and produced findings. Your job is adversarial to the reviewer's: try to REFUTE every finding. You are not asked to agree, to soften, or to add findings of your own.

For each finding, return a verdict:
- REFUTED: you can show the claim is false, or narrower than stated. Declare a TIER for your refutation:
  - REPRODUCED: you provide a POSIX sh SCRIPT that, run from the repository root at this commit, exits 0 if and only if your refutation is demonstrated — for example, it runs the exact input the finding says misbehaves and prints the correct result, or it shows the check the finding says is missing actually firing. The script runs in a DISPOSABLE COPY of the repository, so it may create files inside the tree (a small test or program beside the code, inside the module, run with the language's own tooling) and must not need the network. If your refutation is real, the script exits 0.
  - CODE-READ: an argument from file:line with no execution behind it.
- STANDS: you could not refute it. Say what you tried.

Rules, learned from verifiers being confidently wrong:
- A search that does not find something is NOT a refutation. "I grepped and there is no such path" is CODE-READ at most, and usually STANDS. The two places a reviewer says exist may be a method with a receiver, a generated file, or a name you did not think of.
- A reviewer's script that failed does not make the claim false; it makes the claim unreproduced. Judge the claim.
- A reviewer's script that passed does not make the claim true as stated; check whether the script proves what the claim says, or something narrower.
- Refute the claim that was made, on the input it names. A different input is a different claim.

Return ONE JSON object and nothing else:
{
  "opinion": "prose: your view of the review as a whole, by finding number",
  "refutations": [
    {"id": "R1", "verdict": "REFUTED|STANDS", "tier": "REPRODUCED|CODE-READ", "argument": "one paragraph", "script": "sh script, REPRODUCED refutations only, else empty"}
  ]
}`

// VerifierBrief composes the verifier's user turn: the review as recorded
// — every finding with its tier, its script and what the script printed —
// then the scope.
func VerifierBrief(r Review, sc Scope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nCommit: %s\nScope: %s\nReviewer: %s\n\n", r.Repo, r.Commit, r.Scope, r.ReviewerModel)
	fmt.Fprintf(&b, "The reviewer's opinion:\n%s\n\nThe findings, as recorded (a finding declared REPRODUCED whose script did not exit 0 is recorded CODE-READ):\n\n", r.Opinion)
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "--- %s [%s", f.ID, f.Tier)
		if f.Tier != f.Declared {
			fmt.Fprintf(&b, ", declared %s: %s", f.Declared, f.Demoted)
		}
		fmt.Fprintf(&b, "] severity %s, %s:%d\n%s\n", f.Severity, f.File, f.Line, f.Claim)
		if f.Script != "" {
			fmt.Fprintf(&b, "script:\n%s\n", f.Script)
			if f.ExitCode != nil {
				fmt.Fprintf(&b, "exit %d, output:\n%s\n", *f.ExitCode, f.Stdout)
			}
		}
		b.WriteString("\n")
	}
	if len(r.Sound) > 0 {
		fmt.Fprintf(&b, "The reviewer listed as checked and sound: %s\n\n", strings.Join(r.Sound, "; "))
	}
	fmt.Fprintf(&b, "The scope, %d file(s):\n\n", len(sc.Files))
	for _, f := range sc.Files {
		fmt.Fprintf(&b, "===== %s =====\n%s\n\n", f, sc.Contents[f])
	}
	return b.String()
}

type verifierReply struct {
	Opinion     string `json:"opinion"`
	Refutations []struct {
		ID       string `json:"id"`
		Verdict  string `json:"verdict"`
		Tier     string `json:"tier"`
		Argument string `json:"argument"`
		Script   string `json:"script"`
	} `json:"refutations"`
}

// ParseRefutations reads the verifier's reply into refutations keyed by
// finding id. An unknown verdict is STANDS (the verifier asserted no
// refutation the run can check); an unknown tier on a REFUTED verdict is
// CODE-READ.
func ParseRefutations(text, model string) (opinion string, byID map[string]Refutation, err error) {
	js := extractJSON(text)
	if js == "" {
		return "", nil, errors.New("review: the verifier's reply holds no JSON object")
	}
	var rp verifierReply
	if uerr := json.Unmarshal([]byte(js), &rp); uerr != nil {
		return "", nil, fmt.Errorf("review: the verifier's reply is not the requested shape: %w", uerr)
	}
	byID = map[string]Refutation{}
	for _, x := range rp.Refutations {
		verdict := strings.ToUpper(strings.TrimSpace(x.Verdict))
		if verdict != VerdictRefuted {
			verdict = VerdictStands
		}
		tier := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(x.Tier)), "_", "-")
		if verdict == VerdictStands {
			tier = ""
		} else if tier != TierReproduced {
			tier = TierCodeRead
		}
		byID[strings.TrimSpace(x.ID)] = Refutation{Model: model, Verdict: verdict, Argument: strings.TrimSpace(x.Argument),
			Declared: tier, Tier: tier, Script: strings.TrimSpace(x.Script)}
	}
	return strings.TrimSpace(rp.Opinion), byID, nil
}

// Verify attaches the verifier's refutations to the findings and runs the
// REPRODUCED ones. The same demote-only rule as Reproduce, applied to the
// refutation: a REPRODUCED refutation whose script did not exit 0 becomes
// CODE-READ on the record. And then the one promotion the design allows
// in this direction — a refutation that REPRODUCED demotes the FINDING it
// refutes to CODE-READ, with the record saying which model refuted it and
// how; a CODE-READ refutation is carried as an opinion and moves nothing.
// The human's adjudication, if any, still outranks all of it.
func Verify(ctx context.Context, rep Reproducer, r *Review, model, opinion string, refs map[string]Refutation) {
	r.VerifierModel, r.VerifierOpinion = model, opinion
	for i := range r.Findings {
		f := &r.Findings[i]
		x, ok := refs[f.ID]
		if !ok {
			continue
		}
		if x.Verdict == VerdictRefuted && x.Declared == TierReproduced {
			if strings.TrimSpace(x.Script) == "" {
				x.Tier, x.Demoted = TierCodeRead, "declared REPRODUCED with no script"
			} else {
				out, code, err := rep.Run(ctx, x.Script)
				x.Stdout = tail(out, 4000)
				switch {
				case err != nil:
					x.Tier, x.Demoted = TierCodeRead, "the script could not run: "+err.Error()
				default:
					x.ExitCode = &code
					if code != 0 {
						x.Tier, x.Demoted = TierCodeRead, fmt.Sprintf("the script exited %d — it did not demonstrate the refutation", code)
					}
				}
			}
		}
		f.Refutation = &x
		if x.Verdict == VerdictRefuted && x.Tier == TierReproduced && f.Tier == TierReproduced {
			f.Tier = TierCodeRead
			f.Demoted = fmt.Sprintf("refuted by %s, reproduced: %s", model, x.Argument)
		}
	}
}

// VerifierAgentBrief is VerifierBrief for an agentic seat: the review as
// recorded, then the scope's file list — the seat reads the tree itself.
func VerifierAgentBrief(r Review, files []string) string {
	head := VerifierBrief(r, Scope{})
	head = strings.TrimSuffix(head, fmt.Sprintf("The scope, %d file(s):\n\n", 0))
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(AgentTreeParagraph("refutation"))
	fmt.Fprintf(&b, "The scope, %d file(s):\n", len(files))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	return b.String()
}
