// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"strings"
	"testing"
)

// Review a906b2676dca (Codex re-attacking the loop's fix, Gemini
// verifying), R2, inverted: an object carrying only an "opinion" key is
// any prose-shaped literal; one placed before the payload replaced the
// payload and dropped every finding. The review's shape is its findings.
func TestParseSkipsAnOpinionOnlyLiteralBeforeThePayload(t *testing.T) {
	text := `Here is my summary first: {"opinion": "looks fine"}

And now the review:
{"opinion":"Add subtracts","findings":[{"claim":"Add subtracts","tier":"REPRODUCED","file":"a.go","line":3,"severity":"high","script":"true"}],"sound":["go.mod"]}`
	opinion, findings, sound, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || opinion != "Add subtracts" || len(sound) != 1 {
		t.Fatalf("the literal before the payload won: %q %d %d", opinion, len(findings), len(sound))
	}
	// A reply that is ONLY an opinion-shaped literal is no review.
	if _, _, _, err := Parse(`{"opinion": "looks fine"}`); err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("an opinion-only object must not parse as a review: %v", err)
	}
}
