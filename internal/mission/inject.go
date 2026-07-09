// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"strings"

	"github.com/pdbethke/corralai/internal/fence"
)

// lookbookRoles are the roles whose work is shaped by a visual design directive.
// Pentester/perf/writer/researcher don't consume a UI style guide.
var lookbookRoles = map[string]bool{"designer": true, "builder": true, "reviewer": true}

// InjectHerdContext prepends a mission's herd context to its phase instructions:
// an "available MCP capabilities" note to EVERY role, and the lookbook design
// directive to the design-shaping roles only. Operator-authored guideline text is
// fenced as untrusted (an operator paste must not smuggle instructions to an
// agent). Empty inputs return the plan unchanged (degrade-never-block).
func InjectHerdContext(plan []PhaseSpec, lookbookGuidelines []string, endpointNames []string) []PhaseSpec {
	if len(lookbookGuidelines) == 0 && len(endpointNames) == 0 {
		return plan
	}
	endpointBlock := ""
	if len(endpointNames) > 0 {
		endpointBlock = "Available MCP capabilities you may use (call via call_capability): " +
			strings.Join(endpointNames, ", ") + "."
	}
	lookbookBlock := ""
	if len(lookbookGuidelines) > 0 {
		lookbookBlock = fence.Untrusted("lookbook design directive", "operator",
			strings.Join(lookbookGuidelines, "\n\n"))
	}
	out := make([]PhaseSpec, len(plan))
	for i, p := range plan {
		var pre []string
		if endpointBlock != "" {
			pre = append(pre, endpointBlock)
		}
		if lookbookBlock != "" && lookbookRoles[p.Role] {
			pre = append(pre, lookbookBlock)
		}
		if len(pre) > 0 {
			p.Instruction = strings.Join(pre, "\n\n") + "\n\n" + p.Instruction
		}
		out[i] = p
	}
	return out
}
