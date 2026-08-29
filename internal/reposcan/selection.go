// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"fmt"

	"github.com/pdbethke/corralai/internal/lang"
)

// SelectionEvidence is what ONE instrumented run of the suite learned about
// which tests execute which files, held for the whole scan so each job can
// ask about its own file without re-running anything. Ran distinguishes "we
// have evidence" from "we could not get any" — and when it is false, Note
// is the reason every file in this scan grades whole-suite, disclosed.
type SelectionEvidence struct {
	Raw  []byte
	Ran  bool
	Note string
}

// CollectSelectionEvidence runs the plugin's Instrument command once in the
// scan's substrate. Never fatal: any refusal or failure becomes a Note,
// because a scan that cannot select still has a real (whole-suite)
// measurement to make — it just has to say which one it made.
func CollectSelectionEvidence(ctx context.Context, runner commandRunner, files map[string]string, p lang.Plugin, testCmd []string) SelectionEvidence {
	sel, ok := p.(lang.TestSelector)
	if !ok {
		return SelectionEvidence{Note: fmt.Sprintf("no selector for %s", p.Name())}
	}
	cmd, ok := sel.Instrument(testCmd)
	if !ok {
		return SelectionEvidence{Note: fmt.Sprintf("%s: cannot instrument test command %v", p.Name(), testCmd)}
	}
	out, err := runner.Enumerate(ctx, files, cmd)
	if err != nil {
		return SelectionEvidence{Note: fmt.Sprintf("%s: selection evidence run failed: %v", p.Name(), err)}
	}
	return SelectionEvidence{Raw: []byte(out), Ran: true}
}

// For answers one file. A whole-suite answer is a Selection with Cmd nil
// and a non-empty Fallback; the caller runs testCmd and records Fallback.
func (e SelectionEvidence) For(p lang.Plugin, repoRoot, codePath, testPath string, testCmd []string) lang.Selection {
	if !e.Ran {
		return lang.Selection{Fallback: e.Note}
	}
	sel, ok := p.(lang.TestSelector)
	if !ok {
		return lang.Selection{Fallback: fmt.Sprintf("no selector for %s", p.Name())}
	}
	s, err := sel.Select(e.Raw, repoRoot, codePath, testPath, testCmd)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}
	}
	return s
}
