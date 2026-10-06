// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/queue"
)

// RunCriticTyped is the critic as ONE typed decision instead of a tool loop:
// the same instruction the loop gets, answered with every test's verdict in a
// fixed shape, in a single call. It is the pool's critic seat (see runCritic,
// which retries it once and falls back to runCriticLoop), and
// scripts/criticbench grades it against the loop on tokens, calls, accuracy
// and stability.
//
// The loop re-sends the code and tests on every step and files one finding
// per tool call; this sends them once. On the bench (2026-10-05,
// qwen3.6:35b-a3b, 5 runs per fixture) it caught 45 of 45 planted vacuous
// tests to the loop's 40, raised no false alarm to the loop's 2, and used
// 5.5x less input. That is a local model on 20 tests, not a cloud seat on a
// real suite.
//
// An answer that does not parse into the shape is a review that did not
// happen: it returns CriticIncompletePrefix and no findings, never a clean
// review, the same rule the loop follows when it runs out of steps.
func RunCriticTyped(model Chatter, instruction string) (string, []queue.Finding, error) {
	out, findings, perr, err := criticTypedOnce(model, instruction)
	if err != nil {
		return "", nil, err
	}
	if perr != nil {
		return fmt.Sprintf("%sthe typed critic's answer did not parse (%v), so no review was recorded", CriticIncompletePrefix, perr), nil, nil
	}
	return out, findings, nil
}

// runCritic is the pool's critic seat: the typed critic, asked once more if
// its answer does not parse, and the tool loop only if the second answer
// does not parse either. The bench saw a 7B model's typed answer fail to
// parse 2 times in 5 on one fixture, and a second ask is far cheaper than the
// loop, which re-sends the code and tests on every step.
//
// A provider error is returned as it is, not retried and not handed to the
// loop: it is not an answer, and the loop would be calling the same provider.
//
// The recorded result says which path produced the review. A loop fallback
// that is itself cut short keeps CriticIncompletePrefix at the front, where
// the driver reads it.
func runCritic(model Chatter, instruction string) (string, []queue.Finding, error) {
	var perrs []string
	for attempt := 0; attempt < 2; attempt++ {
		out, findings, perr, err := criticTypedOnce(model, instruction)
		if err != nil {
			return "", nil, err
		}
		if perr == nil {
			return out, findings, nil
		}
		perrs = append(perrs, perr.Error())
	}
	out, findings, err := runCriticLoop(model, instruction)
	if err != nil {
		return "", nil, err
	}
	note := fmt.Sprintf("the typed critic's answer did not parse twice (%s), so the tool loop reviewed instead: ", strings.Join(perrs, "; "))
	if rest, cut := strings.CutPrefix(out, CriticIncompletePrefix); cut {
		return CriticIncompletePrefix + note + rest, findings, nil
	}
	return note + out, findings, nil
}

// criticTypedOnce makes the one typed call. err is the provider's; perr says
// the answer arrived and did not parse.
func criticTypedOnce(model Chatter, instruction string) (out string, findings []queue.Finding, perr, err error) {
	sys := `You are a TEST CRITIC in an adversarial audit. Judge EVERY test in the developer's test file below, then answer with ONLY this JSON and nothing else:

{"tests":[{"test":"<test name>","verdict":"sound|vacuous|dead_check","reason":"<one sentence>","test_file":"<repo-relative path>","test_selector":"<runnable selector for that one test>"}]}

verdict is "vacuous" when the WHOLE test can never fail, "dead_check" when one check inside it can never fail while the test still asserts something real, and "sound" otherwise. List every test exactly once.

Task: ` + instruction
	m, err := model.Chat([]Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "Judge every test now. Reply with the JSON only."},
	}, nil)
	if err != nil {
		return "", nil, nil, err
	}
	verdicts, perr := parseTypedVerdicts(m.Content)
	if perr != nil {
		return "", nil, perr, nil
	}
	for _, v := range verdicts {
		scope := ""
		switch v.Verdict {
		case "vacuous":
			scope = "whole-test"
		case "dead_check":
			scope = "dead-check"
		default:
			continue
		}
		findings = append(findings, queue.Finding{
			Type: "vacuous_test", Severity: "medium",
			Target: v.Test, Evidence: v.Reason, Scope: scope,
			TestFile: v.TestFile, TestSelector: v.TestSelector,
			Status: queue.FindingOpen, CreatedTS: float64(time.Now().Unix()),
		})
	}
	return fmt.Sprintf("typed critic judged %d test(s), %d flagged", len(verdicts), len(findings)), findings, nil, nil
}

type typedVerdict struct {
	Test         string `json:"test"`
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason"`
	TestFile     string `json:"test_file"`
	TestSelector string `json:"test_selector"`
}

// parseTypedVerdicts reads the typed answer strictly: a JSON object with a
// non-empty tests list, every entry naming a test and one of the three
// verdicts. Anything else is an error, because a half-understood answer
// recorded as a review would be a measurement nobody took.
func parseTypedVerdicts(content string) ([]typedVerdict, error) {
	raw := strings.TrimSpace(content)
	if mt := fence.FindStringSubmatch(raw); mt != nil {
		raw = mt[1]
	}
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		raw = raw[i:]
		if j := strings.LastIndexByte(raw, '}'); j >= 0 {
			raw = raw[:j+1]
		}
	}
	var doc struct {
		Tests []typedVerdict `json:"tests"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("not the expected JSON: %w", err)
	}
	if len(doc.Tests) == 0 {
		return nil, fmt.Errorf("no tests judged")
	}
	for _, v := range doc.Tests {
		if strings.TrimSpace(v.Test) == "" {
			return nil, fmt.Errorf("a verdict names no test")
		}
		switch v.Verdict {
		case "sound", "vacuous", "dead_check":
		default:
			return nil, fmt.Errorf("test %q has verdict %q, not sound, vacuous or dead_check", v.Test, v.Verdict)
		}
	}
	return doc.Tests, nil
}
