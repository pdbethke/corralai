// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestGoalDeriverAcceptsLocalModel pins the fix for `certify --repo` being
// unable to run without a cloud vendor.
//
// The goal deriver routed through agentbackend.ForModel, which is the
// CROSS-VENDOR CLOUD path and refuses an ollama name outright:
//
//	cannot infer a cloud vendor for model "qwen3.5:9b-q8_0"
//
// Every other seat (generator, writer, critic) accepts a local model, so a
// repo scan was the one mode that could not run locally — against the project's
// own local-first claim. --goals sidesteps the deriver, but that is a
// workaround, not the contract.
func TestGoalDeriverAcceptsLocalModel(t *testing.T) {
	// No cloud credentials in scope: a local model must not need one.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	for _, model := range []string{"qwen3.5:9b-q8_0", "gemma4:12b", "qwen2.5-coder:14b"} {
		d, err := newLLMDeriver(model, "")
		if err != nil {
			t.Fatalf("newLLMDeriver(%q) = %v, want a local backend and no error", model, err)
		}
		if d == nil {
			t.Fatalf("newLLMDeriver(%q) returned a nil deriver", model)
		}
	}
}

// A cloud model with no credential must still fail closed — the local fallback
// must not swallow a missing key and silently talk to ollama instead.
func TestGoalDeriverStillFailsClosedForCloudWithoutKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	if os.Getenv("GEMINI_API_KEY") != "" {
		t.Skip("env not isolated")
	}
	if _, err := newLLMDeriver("gemini-3.7-flash", ""); err == nil {
		t.Fatal("newLLMDeriver(cloud model, no key) = nil error, want a refusal")
	}
}

// TestGoalDeriverHonoursThePinnedGatewayAndTheRegistryEndpoint pins the
// router review's finding: the derive seat called ForModelOrLocal, which
// ignores MODEL_BACKEND, so with every grading seat pinned to a gateway
// the derive seat dialled the vendor directly — demanding the vendor's own
// key and sending every candidate's source to a host the operator had
// pinned everything away from. And the registry's endpoint for this seat
// was printed, then discarded.
func TestGoalDeriverHonoursThePinnedGatewayAndTheRegistryEndpoint(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "test-placeholder-not-a-real-key")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/v1")

	// Pinned gateway, no Anthropic key: the grading seats accept this
	// (localChatterFor), and so must the derive seat.
	d, err := newLLMDeriver("claude-sonnet-5", "")
	if err != nil {
		t.Fatalf("with MODEL_BACKEND pinned to a gateway the derive seat still demanded the vendor's key: %v", err)
	}
	if d == nil {
		t.Fatal("nil deriver")
	}

	// The registry placed the seat on a daemon: that daemon, not OLLAMA_URL.
	t.Setenv("OLLAMA_URL", "http://wrong.invalid:11434")
	ld, err := newLLMDeriver("qwen2.5-coder:14b", "http://gpu2.internal:11434")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%+v", ld.(llmDeriver).b); !strings.Contains(got, "gpu2.internal") {
		t.Errorf("derive backend = %s, want the registry's endpoint", got)
	}
}
