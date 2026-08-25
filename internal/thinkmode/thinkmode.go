// SPDX-License-Identifier: Elastic-2.0

// Package thinkmode decides whether an Ollama request must explicitly switch
// off a model's built-in reasoning pass.
//
// Reasoning models put their chain-of-thought in
// a separate `thinking` field and the actual answer in `content`. When the
// token budget runs out mid-reasoning the request still succeeds — HTTP 200,
// no error — but `content` comes back EMPTY with done_reason "length". A caller
// that reads only `content` therefore sees a silent empty string, which is the
// worst possible failure shape for an agent loop: no error to retry on, no
// signal that anything went wrong.
//
// Sending `"think": false` suppresses the reasoning pass and puts the answer
// back in `content` where every caller already looks.
//
// Measured on ollama 0.31.1 (prompt: "Say hello in exactly 5 words", 150-token
// budget), /api/chat:
//
//	qwen3.5:9b-q8_0    thinking=467c  content=0c   done=length   <-- silent empty
//	qwen3:14b          thinking=613c  content=0c   done=length   <-- silent empty
//	deepseek-r1:14b    thinking=600c  content=0c   done=length   <-- silent empty
//	gemma4:12b         thinking=424c  content=0c   done=length   <-- silent empty
//	qwen2.5-coder:14b  thinking=0c    content=32c  done=stop     <-- fine as-is
//
// With "think": false, all four return content and done=stop.
//
// One honest caveat on DeepSeek-R1: "think": false does not fully silence it
// (it still emitted 213 chars of thinking), but it does restore a populated
// content and a clean stop, which is the failure we are actually fixing.
//
// The scope is deliberately narrow: only families we have PROBED are listed.
// `think` is an Ollama-specific field that the OpenAI-compatible and Anthropic
// backends do not accept, and widening it on reputation would be guessing. It
// happens to be harmless on qwen2.5, but "harmless on the one we tested" is not
// a reason to send it to everything.
package thinkmode

import "strings"

// probed lists model-name prefixes measured to route their answer through the
// `thinking` field. Add to it ONLY after probing (see the package doc).
var probed = []string{
	"deepseek-r1", // measured: thinking=600c content=0c done=length
	"gemma4",      // measured: thinking=424c content=0c done=length
}

// Suppress reports whether an Ollama request for model should carry
// "think": false.
//
// True for Qwen major version 3 and above (qwen3, qwen3.5, qwen3.6, qwen3.8,
// qwen3-coder, qwen3-next, qwen3-vl, …) and for each prefix in probed. False
// for everything else, including qwen2.5 and earlier, which have no reasoning
// pass to suppress.
//
// To extend this to another family, PROBE IT FIRST: send a short prompt with a
// small num_predict and check whether `content` comes back empty while
// `thinking` is populated. Do not add a family on reputation alone.
func Suppress(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	// Registry-qualified names ("library/qwen3.5:9b", "hf.co/org/qwen3…").
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	for _, p := range probed {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	rest, ok := strings.CutPrefix(m, "qwen")
	if !ok {
		return false
	}
	// Leading digits are the major version: "3.5:9b-q8_0" -> 3, "2.5-coder" -> 2.
	// A name with no digit here ("qwen:110b", the original Qwen 1) is pre-3.
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	major := 0
	for _, c := range rest[:end] {
		major = major*10 + int(c-'0')
		if major > 99 { // absurd version, treat as unknown rather than overflow
			return false
		}
	}
	return major >= 3
}
