// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/queue"
)

// ResponseFormat asks a backend to constrain its answer to Schema, a JSON
// Schema, by whatever means its provider offers (Ollama's format, the
// OpenAI-compatible response_format, the Responses API's text.format,
// Anthropic's output_config.format). It travels as the only element of a
// Chat call's tools rather than through a new method, because every wrapper
// between a seat and its backend already forwards tools untouched: a
// capability carried on a separate interface is dropped by the first
// wrapper that does not implement it, and nothing says so.
//
// Every object in Schema must set additionalProperties false and list all
// its properties as required; the strictest providers refuse anything else.
type ResponseFormat struct {
	Name   string
	Schema map[string]any
}

// criticFormat is the typed critic's answer shape as a schema: the same
// shape its prompt describes and parseTypedVerdicts enforces, so a provider
// that honours it cannot return a verdict outside the three.
var criticFormat = ResponseFormat{Name: "critic_verdicts", Schema: map[string]any{
	"type": "object", "additionalProperties": false, "required": []any{"tests"},
	"properties": map[string]any{"tests": map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []any{"test", "verdict", "reason", "test_file", "test_selector"},
			"properties": map[string]any{
				"test":          map[string]any{"type": "string"},
				"verdict":       map[string]any{"type": "string", "enum": []any{"sound", "vacuous", "dead_check"}},
				"reason":        map[string]any{"type": "string"},
				"test_file":     map[string]any{"type": "string"},
				"test_selector": map[string]any{"type": "string"},
			},
		},
	}},
}}

// criticListHeader opens the block that hands the critic the tests it must
// judge. CriticTestListBlock writes it and criticTestList reads it back, so
// the driver and the worker cannot disagree about its shape.
const criticListHeader = "TESTS TO JUDGE, each exactly once, keyed by this exact name, in file "

// CriticTestListBlock is the block the driver appends to the critic's task
// when the test file's language can list its tests statically
// (lang.Plugin.TestNamesInSource). With it, the typed critic asks for an
// answer keyed by test name with every name required, so a provider that
// honours the schema cannot skip a test, and an answer that does skip one, or
// names one that is not there, does not parse. No names, no block: the
// critic then answers in the unkeyed shape, as before.
func CriticTestListBlock(testFile string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(criticListHeader + testFile + ":\n")
	for _, n := range names {
		b.WriteString("- " + n + "\n")
	}
	return b.String()
}

// criticTestList reads the block CriticTestListBlock wrote, or "", nil when
// the instruction has none.
func criticTestList(instruction string) (string, []string) {
	_, _, file, names := criticTestListSpan(instruction)
	return file, names
}

// criticTestListSpan locates the list block: its byte range in instruction,
// the file it names and the tests it lists. start is -1 when there is none.
func criticTestListSpan(instruction string) (start, end int, file string, names []string) {
	i := strings.Index(instruction, criticListHeader)
	if i < 0 {
		return -1, -1, "", nil
	}
	rest := instruction[i+len(criticListHeader):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return -1, -1, "", nil
	}
	file = strings.TrimSuffix(rest[:nl], ":")
	end = i + len(criticListHeader) + nl + 1
	for _, line := range strings.SplitAfter(instruction[end:], "\n") {
		n, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "- ")
		if !ok || !strings.HasSuffix(line, "\n") {
			break
		}
		names = append(names, n)
		end += len(line)
	}
	return i, end, file, names
}

// criticBatchSize is the most tests one typed call is asked to judge by name.
// A keyed schema carries one required property per test, and providers cap
// how large a constrained-output schema may compile to: measured 2026-10-06,
// claude-haiku-4-5 accepted 20 names and refused 40 ("The compiled grammar is
// too large"), and gemini-3.8-flash accepted 40 and refused 90 with a bare
// 400. flask's tests/test_basic.py declares about 90 tests, so one schema per
// file failed on both, and the critic fell to the loop. 20 is the largest
// size measured to pass on both.
const criticBatchSize = 20

// criticBatches splits the task into one instruction per batch of at most
// criticBatchSize listed tests, each carrying the whole code and test file
// but listing only its own tests. A task with no list, or a short one, is a
// single batch, unchanged.
func criticBatches(instruction string) []string {
	start, end, file, names := criticTestListSpan(instruction)
	if start < 0 || len(names) <= criticBatchSize {
		return []string{instruction}
	}
	var out []string
	for i := 0; i < len(names); i += criticBatchSize {
		j := min(i+criticBatchSize, len(names))
		out = append(out, instruction[:start]+CriticTestListBlock(file, names[i:j])+instruction[end:])
	}
	return out
}

// keyedFormat is the answer schema when the tests are known: an object with
// one required property per listed test, so skipping a test is outside the
// schema rather than a judgement the model may quietly make.
func keyedFormat(names []string) ResponseFormat {
	verdict := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"verdict", "reason"},
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []any{"sound", "vacuous", "dead_check"}},
			"reason":  map[string]any{"type": "string"},
		},
	}
	props := map[string]any{}
	req := make([]any, len(names))
	for i, n := range names {
		props[n] = verdict
		req[i] = n
	}
	return ResponseFormat{Name: "critic_verdicts_by_test", Schema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"tests"},
		"properties": map[string]any{"tests": map[string]any{
			"type": "object", "additionalProperties": false, "required": req, "properties": props,
		}},
	}}
}

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
// 5.5x less input. On gemini-3.8-flash the same day it caught 45 of 45 to the
// loop's 27, with no false alarm from either, on 4x less input. That is 20
// tests, not a real suite.
//
// An answer that does not parse into the shape is a review that did not
// happen: it returns CriticIncompletePrefix and no findings, never a clean
// review, the same rule the loop follows when it runs out of steps.
func RunCriticTyped(model Chatter, instruction string) (string, []queue.Finding, error) {
	constrained := true
	batches := criticBatches(instruction)
	var all []queue.Finding
	judged := 0
	for i, b := range batches {
		out, findings, perr, err := typedCall(model, b, &constrained)
		if err != nil {
			return "", nil, err
		}
		if perr != nil {
			return fmt.Sprintf("%sthe typed critic's answer%s did not parse (%v), so no review was recorded", CriticIncompletePrefix, batchLabel(i, len(batches)), perr), nil, nil
		}
		if len(batches) == 1 {
			return out, findings, nil
		}
		all = append(all, findings...)
		_, names := criticTestList(b)
		judged += len(names)
	}
	return batchSummary(judged, len(batches), len(all)), all, nil
}

// batchLabel names a batch in a message, or nothing for an unbatched task.
func batchLabel(i, n int) string {
	if n == 1 {
		return ""
	}
	return fmt.Sprintf(" for batch %d of %d", i+1, n)
}

func batchSummary(judged, batches, flagged int) string {
	return fmt.Sprintf("typed critic judged %d test(s) in %d batches, %d flagged", judged, batches, flagged)
}

// typedCall makes one typed critic call, constrained to criticFormat while
// *constrained holds. A provider error on a constrained call is answered with
// one plain call, because a model or server without constrained output
// refuses the request rather than ignoring the schema, and that must not
// leave a seat with no critic. The plain call's outcome stands, and
// *constrained is cleared so a retry does not ask again.
func typedCall(model Chatter, instruction string, constrained *bool) (string, []queue.Finding, error, error) {
	if *constrained {
		out, findings, perr, err := criticTypedOnce(model, instruction, true)
		if err == nil {
			return out, findings, perr, nil
		}
		*constrained = false
	}
	return criticTypedOnce(model, instruction, false)
}

// runCritic is the pool's critic seat: the typed critic, asked once more if
// its answer does not parse, and the tool loop only if the second answer
// does not parse either. The bench saw a 7B model's typed answer fail to
// parse 2 times in 5 on one fixture, and a second ask is far cheaper than the
// loop, which re-sends the code and tests on every step.
//
// Each call asks the provider to hold the answer to criticFormat. A provider
// error on that call gets one plain call (see typedCall); an error on the
// plain call is returned as it is, never handed to the loop: it is not an
// answer, and the loop would be calling the same provider.
//
// The recorded result says which path produced the review. A loop fallback
// that is itself cut short keeps CriticIncompletePrefix at the front, where
// the driver reads it.
func runCritic(model Chatter, instruction string) (string, []queue.Finding, error) {
	constrained := true
	batches := criticBatches(instruction)
	var all []queue.Finding
	judged := 0
	for i, b := range batches {
		var perrs []string
		done := false
		for attempt := 0; attempt < 2 && !done; attempt++ {
			out, findings, perr, err := typedCall(model, b, &constrained)
			if err != nil {
				return "", nil, err
			}
			if perr != nil {
				perrs = append(perrs, perr.Error())
				continue
			}
			if len(batches) == 1 {
				return out, findings, nil
			}
			all = append(all, findings...)
			_, names := criticTestList(b)
			judged += len(names)
			done = true
		}
		if !done {
			// One batch that will not answer is the file's review not
			// happening: the loop reviews the whole file, and the batches
			// already answered are not kept beside it as if they were a
			// review of their own.
			note := fmt.Sprintf("the typed critic's answer%s did not parse twice (%s), so the tool loop reviewed instead: ", batchLabel(i, len(batches)), strings.Join(perrs, "; "))
			return runLoopWithNote(model, instruction, note)
		}
	}
	return batchSummary(judged, len(batches), len(all)), all, nil
}

// runLoopWithNote runs the loop over the whole task and prefixes its result
// with note, keeping CriticIncompletePrefix at the front, where the driver
// reads it, when the loop itself is cut short.
func runLoopWithNote(model Chatter, instruction, note string) (string, []queue.Finding, error) {
	out, findings, err := runCriticLoop(model, instruction)
	if err != nil {
		return "", nil, err
	}
	if rest, cut := strings.CutPrefix(out, CriticIncompletePrefix); cut {
		return CriticIncompletePrefix + note + rest, findings, nil
	}
	return note + out, findings, nil
}

// RunCriticLoop runs the tool loop alone, with no typed call before it. The
// critic seat reaches the loop only as a fallback; scripts/criticbench needs
// it on its own to grade it against the typed critic.
func RunCriticLoop(model Chatter, instruction string) (string, []queue.Finding, error) {
	return runCriticLoop(model, instruction)
}

// criticTypedOnce makes the one typed call. err is the provider's; perr says
// the answer arrived and did not parse, or skipped or invented a listed test.
// constrained asks the provider to hold the answer to its schema: keyed by
// test name when the instruction carries a test list, the unkeyed array
// otherwise.
func criticTypedOnce(model Chatter, instruction string, constrained bool) (out string, findings []queue.Finding, perr, err error) {
	testFile, names := criticTestList(instruction)
	shape := `{"tests":[{"test":"<test name>","verdict":"sound|vacuous|dead_check","reason":"<one sentence>","test_file":"<repo-relative path>","test_selector":"<runnable selector for that one test>"}]}`
	every := "List every test exactly once."
	format := criticFormat
	if len(names) > 0 {
		shape = `{"tests":{"<listed test name>":{"verdict":"sound|vacuous|dead_check","reason":"<one sentence>"}}}`
		every = "Give one entry for EVERY test in the TESTS TO JUDGE list, keyed by its exact name, and no other key."
		format = keyedFormat(names)
	}
	sys := `You are a TEST CRITIC in an adversarial audit. Judge EVERY test in the developer's test file below, then answer with ONLY this JSON and nothing else:

` + shape + `

verdict is "vacuous" when the WHOLE test can never fail, "dead_check" when one check inside it can never fail while the test still asserts something real, and "sound" otherwise. ` + every + `

Task: ` + instruction
	var tools []any
	if constrained {
		tools = []any{format}
	}
	m, err := model.Chat([]Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "Judge every test now. Reply with the JSON only."},
	}, tools)
	if err != nil {
		return "", nil, nil, err
	}
	verdicts, perr := parseTypedVerdicts(m.Content, names)
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
		f := queue.Finding{
			Type: "vacuous_test", Severity: "medium",
			Target: v.Test, Evidence: v.Reason, Scope: scope,
			TestFile: v.TestFile, TestSelector: v.TestSelector,
			Status: queue.FindingOpen, CreatedTS: float64(time.Now().Unix()),
		}
		if len(names) > 0 {
			// The listed name IS the runnable selector, and the file is the
			// one the list came from: neither is left to the model.
			f.TestSelector, f.TestFile = v.Test, testFile
		}
		findings = append(findings, f)
	}
	return fmt.Sprintf("typed critic judged %d test(s), %d flagged", len(verdicts), len(findings)), findings, nil, nil
}

// TypedJudgedTests returns the name of every test a typed critic answer
// judged, read with the critic's own parser. It is for scripts/criticbench,
// which compares the count with the tests actually in the file: an answer
// that judges only some of them parses cleanly and reads as a complete review.
func TypedJudgedTests(content string) ([]string, error) {
	verdicts, err := parseTypedVerdicts(content, nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(verdicts))
	for i, v := range verdicts {
		names[i] = v.Test
	}
	return names, nil
}

type typedVerdict struct {
	Test         string `json:"test"`
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason"`
	TestFile     string `json:"test_file"`
	TestSelector string `json:"test_selector"`
}

// parseTypedVerdicts reads the typed answer strictly: a JSON object whose
// tests are either the unkeyed array or an object keyed by test name, every
// entry naming a test and one of the three verdicts. When want lists the
// tests, the answer must be keyed and hold exactly those names: one skipped
// or invented is an error naming it. Anything else is an error, because a
// half-understood answer recorded as a review would be a measurement nobody
// took.
func parseTypedVerdicts(content string, want []string) ([]typedVerdict, error) {
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
		Tests json.RawMessage `json:"tests"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("not the expected JSON: %w", err)
	}
	var verdicts []typedVerdict
	keyed := false
	switch t := strings.TrimSpace(string(doc.Tests)); {
	case strings.HasPrefix(t, "["):
		if err := json.Unmarshal(doc.Tests, &verdicts); err != nil {
			return nil, fmt.Errorf("not the expected JSON: %w", err)
		}
	case strings.HasPrefix(t, "{"):
		keyed = true
		var byName map[string]typedVerdict
		if err := json.Unmarshal(doc.Tests, &byName); err != nil {
			return nil, fmt.Errorf("not the expected JSON: %w", err)
		}
		order := want
		if len(order) == 0 {
			for n := range byName {
				order = append(order, n)
			}
			sort.Strings(order)
		}
		for _, n := range order {
			if v, ok := byName[n]; ok {
				v.Test = n
				verdicts = append(verdicts, v)
			}
		}
		if len(want) > 0 {
			listed := map[string]bool{}
			var missing, extra []string
			for _, n := range want {
				listed[n] = true
				if _, ok := byName[n]; !ok {
					missing = append(missing, n)
				}
			}
			for n := range byName {
				if !listed[n] {
					extra = append(extra, n)
				}
			}
			sort.Strings(extra)
			if len(missing) > 0 || len(extra) > 0 {
				return nil, fmt.Errorf("judged %d of %d listed tests (missing %v, not listed %v)", len(want)-len(missing), len(want), missing, extra)
			}
		}
	default:
		return nil, fmt.Errorf("no tests judged")
	}
	if len(want) > 0 && !keyed {
		return nil, fmt.Errorf("the tests were listed, so the answer must be keyed by test name")
	}
	if len(verdicts) == 0 {
		return nil, fmt.Errorf("no tests judged")
	}
	for _, v := range verdicts {
		if strings.TrimSpace(v.Test) == "" {
			return nil, fmt.Errorf("a verdict names no test")
		}
		switch v.Verdict {
		case "sound", "vacuous", "dead_check":
		default:
			return nil, fmt.Errorf("test %q has verdict %q, not sound, vacuous or dead_check", v.Test, v.Verdict)
		}
	}
	return verdicts, nil
}
