// SPDX-License-Identifier: Elastic-2.0
package mission

import "testing"

func TestStaffedModelRef(t *testing.T) {
	cases := []struct{ in, wantBackend, wantModel string }{
		{"qwen2.5-coder:7b", "ollama", "qwen2.5-coder:7b"}, // the bug: must NOT become ("qwen2.5-coder","7b")
		{"llama3.2:3b", "ollama", "llama3.2:3b"},
		{"claude-3-5-sonnet", "anthropic", "claude-3-5-sonnet"},
		{"gpt-4o", "openai", "gpt-4o"},
	}
	for _, c := range cases {
		got := staffedModelRef(c.in)
		if got.Backend != c.wantBackend || got.Model != c.wantModel {
			t.Errorf("staffedModelRef(%q) = {%q,%q}, want {%q,%q}", c.in, got.Backend, got.Model, c.wantBackend, c.wantModel)
		}
	}
}
