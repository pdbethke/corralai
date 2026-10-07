// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type replyChatter struct {
	reply string
	calls int
}

func (c *replyChatter) Chat(messages []Message, tools []any) (Message, error) {
	c.calls++
	return Message{Role: "assistant", Content: c.reply}, nil
}

// The typed critic answers in ONE call with every test's verdict. Vacuous and
// dead-check verdicts become findings with the right scope; sound ones do
// not.
func TestTypedCriticMapsVerdictsToFindings(t *testing.T) {
	c := &replyChatter{reply: "```json\n" + `{"tests":[
	  {"test":"TestA","verdict":"sound","reason":"checks the sum"},
	  {"test":"TestB","verdict":"vacuous","reason":"asserts nothing","test_file":"pkg/a_test.go","test_selector":"TestB"},
	  {"test":"TestC","verdict":"dead_check","reason":"second check is x==x","test_file":"pkg/a_test.go","test_selector":"TestC"}
	]}` + "\n```"}
	out, findings, err := RunCriticTyped(c, "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Fatalf("the typed critic is one call, made %d", c.calls)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("a parsed answer is complete: %q", out)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 findings (TestB, TestC), got %+v", findings)
	}
	if findings[0].Target != "TestB" || findings[0].Scope != "whole-test" || findings[0].TestSelector != "TestB" {
		t.Errorf("vacuous finding: %+v", findings[0])
	}
	if findings[1].Target != "TestC" || findings[1].Scope != "dead-check" {
		t.Errorf("dead-check finding: %+v", findings[1])
	}
}

// An answer that does not parse is a review that did not happen: incomplete,
// with no findings, and never "clean".
func TestTypedCriticThatDoesNotParseIsIncomplete(t *testing.T) {
	for _, reply := range []string{"", "the tests look fine to me", `{"tests":[{"test":"TestA","verdict":"maybe"}]}`} {
		out, findings, err := RunCriticTyped(&replyChatter{reply: reply}, "critique tests")
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		if !strings.HasPrefix(out, CriticIncompletePrefix) || len(findings) != 0 {
			t.Errorf("reply %q: want an incomplete review with no findings, got %q / %d findings", reply, out, len(findings))
		}
	}
}

// A suite judged entirely sound is a complete review with no findings.
func TestTypedCriticAllSoundIsAClean(t *testing.T) {
	out, findings, err := RunCriticTyped(&replyChatter{reply: `{"tests":[{"test":"TestA","verdict":"sound","reason":"ok"}]}`}, "x")
	if err != nil || len(findings) != 0 || strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("all-sound: %q %d %v", out, len(findings), err)
	}
}

// The pool's critic seat is the typed critic: a reply that parses is the
// review, in one call, and the loop never runs.
func TestRunRoleCriticIsTyped(t *testing.T) {
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: `{"tests":[{"test":"TestA","verdict":"sound","reason":"ok"},{"test":"TestB","verdict":"vacuous","reason":"asserts nothing"}]}`},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatalf("a parsed typed answer is the whole review; Chat was called %d times", fake.calls)
	}
	if len(findings) != 1 || findings[0].Target != "TestB" {
		t.Fatalf("findings = %+v, want TestB", findings)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("a parsed answer is complete: %q", out)
	}
}

// An answer that does not parse is asked for once more before anything else
// happens, and a second answer that parses is the review.
func TestRunRoleCriticRetriesAnUnparseableAnswerOnce(t *testing.T) {
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: "the tests look fine"},
		{Role: "assistant", Content: `{"tests":[{"test":"TestB","verdict":"vacuous","reason":"asserts nothing"}]}`},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Fatalf("want the typed call and one retry, got %d calls", fake.calls)
	}
	if len(findings) != 1 || strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("the retry's parsed answer is the review: %q, %+v", out, findings)
	}
}

// Two unparseable answers hand the review to the tool loop, and the recorded
// result says the loop is what produced it.
func TestRunRoleCriticFallsBackToTheLoop(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"type": "vacuous_test", "severity": "medium", "target": "TestB", "evidence": "asserts nothing"})
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: "not json"},
		{Role: "assistant", Content: "still not json"},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "report_finding", Arguments: raw}}},
		{Role: "assistant", Content: "filed 1"},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 4 {
		t.Fatalf("want 2 typed calls then a 2-step loop, got %d calls", fake.calls)
	}
	if len(findings) != 1 || findings[0].Target != "TestB" {
		t.Fatalf("the loop's findings are the review: %+v", findings)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) || !strings.Contains(out, "tool loop") || !strings.Contains(out, "filed 1") {
		t.Fatalf("the result must say the loop produced it, and carry its summary: %q", out)
	}
}

// A loop fallback that is itself cut short keeps the incomplete marker at the
// FRONT, where the driver reads it.
func TestRunRoleCriticFallbackThatIsCutShortStaysIncomplete(t *testing.T) {
	out, _, err := RunRole(context.Background(), &alwaysThoughtChatter{}, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("an incomplete fallback must lead with the marker: %q", out)
	}
}

type errChatter struct{ calls int }

func (c *errChatter) Chat([]Message, []any) (Message, error) {
	c.calls++
	return Message{}, errors.New("provider down")
}

// A provider error is not an unparseable answer: it is returned, never handed
// to the loop. Only a rejected request gets a plain call (see
// TestTypedCriticDropsTheSchemaOnlyForARejectedBatch).
func TestRunRoleCriticReturnsAProviderError(t *testing.T) {
	c := &errChatter{}
	if _, _, err := RunRole(context.Background(), c, "test-critic", "critique tests"); err == nil {
		t.Fatal("a provider error must be returned")
	}
	if c.calls != 1 {
		t.Fatalf("a provider error that is not a rejection is returned at once; Chat was called %d times", c.calls)
	}
}

// TypedJudgedTests reads the same strict shape the critic does and returns
// every test the answer judged, so a bench can compare it with the file's own
// count; an answer that does not parse is an error, not an empty list.
func TestTypedJudgedTestsListsEveryJudgedTest(t *testing.T) {
	got, err := TypedJudgedTests("```json\n" + `{"tests":[{"test":"TestA","verdict":"sound"},{"test":"TestB","verdict":"vacuous"}]}` + "\n```")
	if err != nil || strings.Join(got, ",") != "TestA,TestB" {
		t.Fatalf("got %v, %v; want [TestA TestB]", got, err)
	}
	if _, err := TypedJudgedTests("the tests look fine"); err == nil {
		t.Fatal("an unparseable answer must be an error")
	}
}

type toolsChatter struct {
	reply string
	tools [][]any
}

func (c *toolsChatter) Chat(_ []Message, tools []any) (Message, error) {
	c.tools = append(c.tools, tools)
	return Message{Role: "assistant", Content: c.reply}, nil
}

// The typed critic asks the provider to hold its answer to the schema: the
// call carries exactly one ResponseFormat, whose verdict field is the closed
// set of three, so a backend that supports constrained output cannot return
// anything else.
func TestTypedCriticAsksForItsSchema(t *testing.T) {
	c := &toolsChatter{reply: `{"tests":[{"test":"TestA","verdict":"sound","reason":"ok","test_file":"a_test.go","test_selector":"TestA"}]}`}
	if _, _, err := RunRole(context.Background(), c, "test-critic", "critique tests"); err != nil {
		t.Fatal(err)
	}
	if len(c.tools) != 1 || len(c.tools[0]) != 1 {
		t.Fatalf("want one call carrying one ResponseFormat, got %#v", c.tools)
	}
	rf, ok := c.tools[0][0].(ResponseFormat)
	if !ok || rf.Name == "" {
		t.Fatalf("the call's tools are not a named ResponseFormat: %#v", c.tools[0][0])
	}
	b, _ := json.Marshal(rf.Schema)
	if !strings.Contains(string(b), `"enum":["sound","vacuous","dead_check"]`) || !strings.Contains(string(b), `"additionalProperties":false`) {
		t.Fatalf("schema must close the verdict set and every object: %s", b)
	}
}

// schemaErrChatter refuses any call that carries a schema, the way a model
// or server without constrained output answers one, and serves a plain call.
type schemaErrChatter struct{ calls, schemaCalls int }

func (c *schemaErrChatter) Chat(_ []Message, tools []any) (Message, error) {
	c.calls++
	if len(tools) > 0 {
		c.schemaCalls++
		return Message{}, fmt.Errorf("400: response_format is not supported for this model: %w", ErrRequestRejected)
	}
	return Message{Role: "assistant", Content: `{"tests":[{"test":"TestB","verdict":"vacuous","reason":"asserts nothing","test_file":"b_test.go","test_selector":"TestB"}]}`}, nil
}

// A provider that cannot constrain its output still gets a typed review: the
// constrained call's error is answered with one plain call, not with a dead
// critic.
func TestTypedCriticWithoutConstrainedOutputStillReviews(t *testing.T) {
	c := &schemaErrChatter{}
	out, findings, err := RunRole(context.Background(), c, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if c.schemaCalls != 1 || c.calls != 2 {
		t.Fatalf("want one constrained call then one plain call, got %d calls (%d constrained)", c.calls, c.schemaCalls)
	}
	if len(findings) != 1 || strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("the plain call's answer is the review: %q %+v", out, findings)
	}
}

// The block the driver writes into the critic's task is the block the critic
// reads back: one helper writes it and one parses it.
func TestCriticTestListRoundTrips(t *testing.T) {
	instr := "critique tests\n\n" + CriticTestListBlock("p/p_test.go", []string{"TestA", "TestB"}) + "\nmore text"
	file, names := criticTestList(instr)
	if file != "p/p_test.go" || strings.Join(names, ",") != "TestA,TestB" {
		t.Fatalf("got %q %v", file, names)
	}
	if CriticTestListBlock("p", nil) != "" {
		t.Fatal("no names, no block")
	}
	if f, n := criticTestList("critique tests"); f != "" || n != nil {
		t.Fatalf("no block reads as no list, got %q %v", f, n)
	}
}

// Handed a list, the critic asks for an answer keyed by test name with every
// name required, so a provider that honours the schema cannot skip a test;
// each finding names its test by the listed selector and file.
func TestTypedCriticWithAListRequiresEveryTest(t *testing.T) {
	instr := "critique tests\n\n" + CriticTestListBlock("p/p_test.go", []string{"TestA", "TestB"})
	c := &toolsChatter{reply: `{"tests":{"TestA":{"verdict":"sound","reason":"checks the sum"},"TestB":{"verdict":"vacuous","reason":"asserts nothing"}}}`}
	out, findings, err := RunRole(context.Background(), c, "test-critic", instr)
	if err != nil {
		t.Fatal(err)
	}
	rf := c.tools[0][0].(ResponseFormat)
	b, _ := json.Marshal(rf.Schema)
	if !strings.Contains(string(b), `"required":["TestA","TestB"]`) {
		t.Fatalf("every listed test must be required by the schema: %s", b)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) || len(findings) != 1 {
		t.Fatalf("a complete keyed answer is the review: %q %+v", out, findings)
	}
	if f := findings[0]; f.Target != "TestB" || f.TestSelector != "TestB" || f.TestFile != "p/p_test.go" || f.Scope != "whole-test" {
		t.Fatalf("finding = %+v", f)
	}
}

// A keyed answer that skips a listed test, or names one that is not listed,
// is not a review: it is retried, then handed to the loop, and the recorded
// result names what was missing.
func TestTypedCriticWithAListRejectsAnIncompleteAnswer(t *testing.T) {
	instr := "critique tests\n\n" + CriticTestListBlock("p/p_test.go", []string{"TestA", "TestB"})
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: `{"tests":{"TestA":{"verdict":"vacuous","reason":"x"}}}`},
		{Role: "assistant", Content: `{"tests":{"TestA":{"verdict":"sound","reason":"x"},"TestB":{"verdict":"sound","reason":"x"},"TestGhost":{"verdict":"vacuous","reason":"x"}}}`},
		{Role: "assistant", Content: "no vacuous tests"},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", instr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tool loop") || !strings.Contains(out, "TestB") || !strings.Contains(out, "TestGhost") {
		t.Fatalf("the result must say the loop reviewed and why: %q", out)
	}
	if len(findings) != 0 {
		t.Fatalf("the skipping answer's finding must not survive: %+v", findings)
	}
}

// The bench reads a keyed answer's judged tests too.
func TestTypedJudgedTestsReadsAKeyedAnswer(t *testing.T) {
	got, err := TypedJudgedTests(`{"tests":{"TestB":{"verdict":"sound","reason":"x"},"TestA":{"verdict":"vacuous","reason":"y"}}}`)
	if err != nil || strings.Join(got, ",") != "TestA,TestB" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// batchChatter answers each typed call with exactly the names its schema
// requires, flagging the ones in vacuous, and records each call's required
// list. skip drops one name from every answer, the way a model that loses
// count of a long list does.
type batchChatter struct {
	vacuous  map[string]bool
	required [][]string
	skip     bool
	loopDone bool
}

func (c *batchChatter) Chat(_ []Message, tools []any) (Message, error) {
	var rf ResponseFormat
	ok := len(tools) == 1
	if ok {
		rf, ok = tools[0].(ResponseFormat)
	}
	if !ok {
		// The loop (its own tools, not a format), after the typed path gave
		// up: conclude at once.
		c.loopDone = true
		return Message{Role: "assistant", Content: "no vacuous tests"}, nil
	}
	req := rf.Schema["properties"].(map[string]any)["tests"].(map[string]any)["required"].([]any)
	var names []string
	ans := map[string]any{}
	for i, r := range req {
		n := r.(string)
		names = append(names, n)
		if c.skip && i == 0 {
			continue
		}
		v := "sound"
		if c.vacuous[n] {
			v = "vacuous"
		}
		ans[n] = map[string]any{"verdict": v, "reason": "x"}
	}
	c.required = append(c.required, names)
	b, _ := json.Marshal(map[string]any{"tests": ans})
	return Message{Role: "assistant", Content: string(b)}, nil
}

func manyTests(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("TestCase%02d", i)
	}
	return out
}

// A list longer than criticBatchSize is judged in batches, each its own
// keyed schema: both providers measured refuse one schema with every name of
// a 90-test file ("compiled grammar is too large" on Anthropic past 20
// names, a 400 on Gemini at 90). Every test is still judged once, and the
// findings of all batches come back.
func TestTypedCriticJudgesALongListInBatches(t *testing.T) {
	names := manyTests(45)
	instr := "critique tests\n\n" + CriticTestListBlock("p/p_test.go", names)
	c := &batchChatter{vacuous: map[string]bool{"TestCase03": true, "TestCase44": true}}
	out, findings, err := RunRole(context.Background(), c, "test-critic", instr)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.required) != 3 || len(c.required[0]) != criticBatchSize || len(c.required[1]) != criticBatchSize || len(c.required[2]) != 5 {
		t.Fatalf("want batches of %d, %d and 5, got %d batches: %v", criticBatchSize, criticBatchSize, len(c.required), c.required)
	}
	var all []string
	for _, b := range c.required {
		all = append(all, b...)
	}
	if strings.Join(all, ",") != strings.Join(names, ",") {
		t.Fatal("every listed test must be judged exactly once, in order")
	}
	if len(findings) != 2 || strings.HasPrefix(out, CriticIncompletePrefix) || c.loopDone {
		t.Fatalf("both batches' findings, no loop: %q %+v", out, findings)
	}
	if !strings.Contains(out, "3 batch") {
		t.Fatalf("the result says how the review was split: %q", out)
	}
}

// A batch that will not answer completely sends the file to the loop, as a
// single unbatched answer would.
func TestTypedCriticBatchThatSkipsFallsBackToTheLoop(t *testing.T) {
	instr := "critique tests\n\n" + CriticTestListBlock("p/p_test.go", manyTests(25))
	c := &batchChatter{skip: true}
	out, _, err := RunRole(context.Background(), c, "test-critic", instr)
	if err != nil {
		t.Fatal(err)
	}
	if !c.loopDone || !strings.Contains(out, "tool loop") {
		t.Fatalf("a batch that keeps skipping must hand the file to the loop: %q", out)
	}
}

// The list block is read from the END of the task: the code or test file
// under review can itself contain the header (a self-audit of this very
// file does), and the first copy must not win.
func TestCriticTestListReadsTheLastBlock(t *testing.T) {
	body := "CODE UNDER REVIEW:\n" + criticListHeader + "fake.go:\n- TestBogus\n\nmore code\n\n"
	instr := body + CriticTestListBlock("p/p_test.go", []string{"TestA", "TestB"})
	file, names := criticTestList(instr)
	if file != "p/p_test.go" || strings.Join(names, ",") != "TestA,TestB" {
		t.Fatalf("got %q %v; the last block is the list", file, names)
	}
	if b := criticBatches(body + CriticTestListBlock("p/p_test.go", manyTests(25))); len(b) != 2 || !strings.HasPrefix(b[0], body) {
		t.Fatal("batching must replace the last block and leave the reviewed source intact")
	}
}

// A name listed twice is one test: listed once, judged once, flagged once.
func TestCriticTestListBlockDropsDuplicates(t *testing.T) {
	_, names := criticTestList(CriticTestListBlock("p.py", []string{"p.py::test_a", "p.py::test_b", "p.py::test_a"}))
	if strings.Join(names, ",") != "p.py::test_a,p.py::test_b" {
		t.Fatalf("got %v", names)
	}
}

// rejectOnceChatter refuses the first constrained call as a REJECTED request,
// then answers every call (constrained or not) with the names it is asked for.
type rejectOnceChatter struct {
	batchChatter
	rejected bool
	plain    int
}

func (c *rejectOnceChatter) Chat(m []Message, tools []any) (Message, error) {
	if len(tools) == 1 {
		if _, ok := tools[0].(ResponseFormat); ok && !c.rejected {
			c.rejected = true
			return Message{}, fmt.Errorf("400 Bad Request: grammar too large: %w", ErrRequestRejected)
		}
	}
	if len(tools) == 0 {
		c.plain++
		return Message{Role: "assistant", Content: `{"tests":{` + strings.Join(func() []string {
			var out []string
			for i := 0; i < criticBatchSize; i++ {
				out = append(out, fmt.Sprintf(`"TestCase%02d":{"verdict":"sound","reason":"x"}`, i))
			}
			return out
		}(), ",") + `}}`}, nil
	}
	return c.batchChatter.Chat(m, tools)
}

// Only a request the provider REJECTED (a 4xx that is not a rate limit)
// drops the schema, and only for that batch: the next batch asks for it again.
func TestTypedCriticDropsTheSchemaOnlyForARejectedBatch(t *testing.T) {
	c := &rejectOnceChatter{}
	instr := "critique tests\n\n" + CriticTestListBlock("p/p_test.go", manyTests(25))
	if _, _, err := RunRole(context.Background(), c, "test-critic", instr); err != nil {
		t.Fatal(err)
	}
	if c.plain != 1 {
		t.Fatalf("want one plain call for the rejected batch, got %d", c.plain)
	}
	if len(c.required) != 1 || len(c.required[0]) != 5 {
		t.Fatalf("the second batch must ask for its schema again: %v", c.required)
	}
}

// A rate limit, a 5xx or a timeout is not a rejection of the schema: it is
// returned as it is, with no plain re-send of a large prompt.
func TestTypedCriticReturnsATransientErrorWithoutAPlainResend(t *testing.T) {
	c := &errChatter{}
	if _, _, err := RunRole(context.Background(), c, "test-critic", "critique tests"); err == nil {
		t.Fatal("a provider error must be returned")
	}
	if c.calls != 1 {
		t.Fatalf("a transient error is not re-sent without the schema; Chat was called %d times", c.calls)
	}
}
