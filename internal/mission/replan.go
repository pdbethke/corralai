// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"fmt"
	"log"

	"github.com/pdbethke/corralai/internal/fence"
	"github.com/pdbethke/corralai/internal/queue"
)

// reflexRules maps a finding to deterministic remediation tasks (the reflex
// tier): a fix task and a dependent re-verify task. Returns actionable=false for
// findings that need judgment (design-flaw, note) — those are left to the LLM
// lead in sub-project #4. Task keys are finding-id-scoped so remediation is
// unique and idempotent.
func reflexRules(f queue.Finding) (specs []queue.TaskSpec, actionable bool) {
	verifyRole := "tester"
	if f.Type == "vuln" {
		verifyRole = "pentester"
	}
	switch f.Type {
	case "vuln", "bug", "regression", "missing-req":
		fixKey := fmt.Sprintf("fix-f%d", f.ID)
		verKey := fmt.Sprintf("verify-f%d", f.ID)
		fixInstr := reflexFixInstr(f)
		verInstr := reflexVerifyInstr(f)
		return []queue.TaskSpec{
			{Key: fixKey, Role: "builder", Title: "fix: " + f.Type, Instruction: fixInstr},
			{Key: verKey, Role: verifyRole, Title: "re-verify: " + f.Type, Instruction: verInstr, DependsOn: []string{fixKey}},
		}, true
	default: // design-flaw, note → judgment, deferred to #4
		return nil, false
	}
}

// reflexFixInstr builds the instruction string for a reflex fix task.
// Brain-derived fields (Type, Severity, Target) are trusted and interpolated
// directly; agent-authored fields (Evidence, SuggestedAction) are wrapped in a
// fence.Untrusted block so a consuming builder bee cannot mistake adversarial
// content inside them for authoritative task instructions.
func reflexFixInstr(f queue.Finding) string {
	untrusted := fence.Untrusted("reported finding evidence", "agent report",
		"Evidence: "+orNone(f.Evidence)+"\nSuggested fix: "+orNone(f.SuggestedAction))
	// write_file (full corrected content) actually repairs the artifact;
	// edit_file only APPENDS an annotation — steering bees to it guaranteed the
	// same finding recurred forever (the fix never landed).
	return fmt.Sprintf("Fix this %s (severity %s) in %s. %s\nApply the fix with write_file — write the file's full corrected content, not a note about it.",
		f.Type, f.Severity, orNone(f.Target), untrusted)
}

// reflexVerifyInstr builds the instruction string for a reflex re-verify task.
// As with reflexFixInstr, brain-derived fields (Type, Severity, Target) are
// trusted and interpolated directly, while the agent-authored Evidence is
// wrapped in a fence.Untrusted block so a bee cannot smuggle a directive to
// the re-verifying bee through a reported finding. (f.Target is a file path,
// low injection risk — left outside as a documented follow-up.)
func reflexVerifyInstr(f queue.Finding) string {
	untrusted := fence.Untrusted("reported finding evidence", "agent report",
		"Evidence: "+orNone(f.Evidence))
	return fmt.Sprintf(
		"Re-verify that the %s in %s (originally severity %s) is resolved. %s\n"+
			"If it is NOT fixed, call report_finding again so the swarm re-plans.",
		f.Type, orNone(f.Target), f.Severity, untrusted)
}

func orNone(s string) string {
	if s == "" {
		return "(none given)"
	}
	return s
}

// replan turns a mission's open, actionable findings (at/above the severity
// threshold) into remediation tasks and marks each addressed — the reflex half
// of the adaptive loop. Deterministic, idempotent, and bounded by ReflexMaxTasks.
func (e *Engine) replan(missionID int64) error {
	findings, err := e.q.Findings(missionID, queue.FindingOpen)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return nil
	}

	// Count existing reflex tasks so the loop can't run away.
	existing, err := e.q.List(missionID)
	if err != nil {
		return err
	}
	// Count only OPEN reflex tasks. The cap bounds IN-FLIGHT, non-converging
	// remediation — a mission that legitimately completed many fix→verify cycles
	// must not be failed for its lifetime throughput. Terminal reflex tasks
	// (done/cancelled/superseded) are converged history, not runaway.
	reflexCount := 0
	for _, t := range existing {
		if !isReflexTask(t.Key) {
			continue
		}
		switch t.Status {
		case queue.StatusDone, queue.StatusCancelled, queue.StatusSuperseded:
			// terminal — do not count
		default:
			reflexCount++
		}
	}

	minRank := queue.SeverityRank(e.ReflexMinSeverity)
	// Findings come newest-first; address oldest-first so the loop is stable.
	for i := len(findings) - 1; i >= 0; i-- {
		f := findings[i]
		if queue.SeverityRank(f.Severity) < minRank {
			continue // below threshold: recorded, not auto-remediated (left for #4)
		}
		// A dep-sweep blocker (cancelled work chain) is a STRUCTURAL failure, not an
		// auto-remediable defect. Leave it OPEN so the convergence gate holds the
		// mission at needs-review (the human gate) instead of auto-addressing it into
		// a false convergence that opens a PR over dead work.
		if f.Reporter == "dep-sweep" {
			continue
		}
		specs, actionable := reflexRules(f)
		if !actionable {
			continue
		}
		// Deduplicate recurring findings: if a reflex fix for this type+target is
		// already in flight, this report rides that remediation instead of
		// spawning another pair (nine reports of one broken go.mod = one fix).
		if f.Target != "" {
			if inFlight, err := e.q.OpenRemediationExists(missionID, f.Type, f.Target); err == nil && inFlight {
				log.Printf("mission %d: finding %d (%s/%s on %s) duplicates in-flight remediation — riding it",
					missionID, f.ID, f.Type, f.Severity, f.Target)
				if _, err := e.q.SetFindingStatus(f.ID, queue.FindingAddressed); err != nil {
					log.Printf("mission %d: mark duplicate finding %d addressed: %v", missionID, f.ID, err)
				} else if e.OnFindingResolved != nil {
					e.OnFindingResolved(f, queue.FindingAddressed)
				}
				continue
			}
		}
		if reflexCount+len(specs) > e.ReflexMaxTasks {
			log.Printf("mission %d: reflex task cap (%d) reached — finding %d (%s/%s) not auto-remediated",
				missionID, e.ReflexMaxTasks, f.ID, f.Type, f.Severity)
			if e.OnReflexCapExhausted != nil {
				e.OnReflexCapExhausted(missionID, e.ReflexMaxTasks, f)
			}
			// Non-converging: the cap's worth of remediation cycles ran and findings
			// are still open. Degrade to the terminal `failed` state rather than a
			// pause that resume just re-hits (the paused-forever oscillation). #5.
			if mi, merr := e.m.Mission(missionID); merr == nil && mi != nil {
				e.failMission(mi, "reflex cap exhausted — mission is not converging")
			}
			break
		}
		if err := e.q.Enqueue(missionID, specs); err != nil {
			log.Printf("mission %d: reflex enqueue for finding %d: %v", missionID, f.ID, err)
			continue
		}
		reflexCount += len(specs)
		if _, err := e.q.SetFindingStatus(f.ID, queue.FindingAddressed); err != nil {
			log.Printf("mission %d: mark finding %d addressed: %v", missionID, f.ID, err)
		} else if e.OnFindingResolved != nil {
			e.OnFindingResolved(f, queue.FindingAddressed)
		}
	}
	return nil
}

func isReflexTask(key string) bool {
	return len(key) >= 4 && (key[:4] == "fix-" || (len(key) >= 7 && key[:7] == "verify-"))
}
