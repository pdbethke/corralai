// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// findingsChatter is a critic that finds `vacuous` vacuous tests and files
// them `perCall` at a time (one tool call each, all in the same reply), then
// concludes with a summary.
type findingsChatter struct {
	vacuous, perCall, filed, calls int
}

func (c *findingsChatter) Chat(messages []Message, tools []any) (Message, error) {
	c.calls++
	if c.filed >= c.vacuous {
		return Message{Role: "assistant", Content: fmt.Sprintf("%d vacuous tests", c.vacuous)}, nil
	}
	var calls []ToolCall
	for i := 0; i < c.perCall && c.filed < c.vacuous; i++ {
		c.filed++
		raw, _ := json.Marshal(map[string]any{
			"type": "vacuous_test", "severity": "medium",
			"target": fmt.Sprintf("TestVacuous%d", c.filed), "evidence": "asserts nothing",
		})
		calls = append(calls, ToolCall{Name: "report_finding", Arguments: raw})
	}
	return Message{Role: "assistant", ToolCalls: calls}, nil
}

// TestCriticFindingsAreNeverSilentlyCapped: the critic's loop used to read
// ONE tool call per reply and stop after critFreeformSteps replies, so a
// suite with more vacuous tests than that reported at most six findings,
// and its recorded summary was a canned default that read as a complete
// review. Every tool call in a reply now counts, and a review the cap cut
// short says so.
func TestCriticFindingsAreNeverSilentlyCapped(t *testing.T) {
	for _, tc := range []struct {
		name                string
		vacuous, perCall    int
		wantAll, wantMarked bool
	}{
		{"three, one per reply", 3, 1, true, false},
		{"ten, all in one reply", 10, 10, true, false},
		{"ten, five per reply", 10, 5, true, false},
		{"ten, one per reply: the cap bites, and says so", 10, 1, false, true},
	} {
		fake := &findingsChatter{vacuous: tc.vacuous, perCall: tc.perCall}
		out, findings, err := runCriticLoop(fake, "critique tests:\n<the test source>")
		if err != nil {
			t.Fatalf("%s: runCriticLoop: %v", tc.name, err)
		}
		if tc.wantAll && len(findings) != tc.vacuous {
			t.Errorf("%s: %d of %d findings came back", tc.name, len(findings), tc.vacuous)
		}
		marked := strings.HasPrefix(out, CriticIncompletePrefix)
		if marked != tc.wantMarked {
			t.Errorf("%s: incomplete marker = %v, want %v (result %q)", tc.name, marked, tc.wantMarked, out)
		}
	}
}
