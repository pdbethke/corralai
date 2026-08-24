// SPDX-License-Identifier: Elastic-2.0

package thinkmode

import "testing"

func TestSuppress(t *testing.T) {
	cases := []struct {
		model string
		want  bool
		why   string
	}{
		// Probed against ollama 0.31.1 — these returned empty content.
		{"qwen3.5:9b-q8_0", true, "measured: thinking=467c content=0c"},
		{"qwen3:14b", true, "measured: thinking=613c content=0c"},
		// Same family, not individually probed but same reasoning architecture.
		{"qwen3.6:27b", true, "qwen3 line"},
		{"qwen3.8:27b", true, "qwen3 line"},
		{"qwen3-coder:30b", true, "qwen3 line"},
		{"qwen3-next:80b", true, "qwen3 line"},
		{"qwen3.5:35b-a3b-q4_K_M", true, "full tag with quant suffix"},

		// Probed: routes its answer through the `thinking` field too.
		{"deepseek-r1:14b", true, "measured: thinking=600c content=0c"},
		{"deepseek-r1:8b", true, "same family"},
		{"deepseek-r1", true, "bare name"},

		// Probed: no thinking field, works as-is.
		{"qwen2.5-coder:14b", false, "measured: thinking=0c content=32c"},
		{"qwen2.5:32b", false, "pre-3"},
		{"qwen2:7b", false, "pre-3"},
		{"qwen:110b", false, "original Qwen, no major digit"},

		// Other families are out of scope by design.
		{"llama3.3:70b", false, "not qwen — llama3 must not match on the 3"},
		{"mistral:7b", false, "not qwen"},
		{"deepseek-v3:latest", false, "different deepseek line, NOT probed"},
		{"deepseek-coder:6.7b", false, "different deepseek line, NOT probed"},
		{"", false, "empty"},

		// Normalisation.
		{"  QWEN3.5:9B  ", true, "case and surrounding space"},
		{"library/qwen3.5:9b", true, "registry-qualified"},
		{"hf.co/Qwen/qwen3.5:9b", true, "hf-qualified"},
		{"library/qwen2.5:7b", false, "registry-qualified, pre-3"},
	}
	for _, c := range cases {
		if got := Suppress(c.model); got != c.want {
			t.Errorf("Suppress(%q) = %v, want %v (%s)", c.model, got, c.want, c.why)
		}
	}
}

// A model name that merely CONTAINS "qwen" downstream must not match — the
// prefix is the whole point, otherwise "not-qwen3" or a vendor prefix would
// silently enable the flag.
func TestSuppressRequiresPrefix(t *testing.T) {
	for _, m := range []string{"notqwen3:9b", "myqwen3", "xqwen3.5:9b", "my-deepseek-r1"} {
		if Suppress(m) {
			t.Errorf("Suppress(%q) = true, want false — must match a qwen PREFIX, not a substring", m)
		}
	}
}
