// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/agentrole"
)

// TestRunQueueLoopBuildsClaimArgsFromRoleSet is the multi-role fix's core
// regression guard for the agent side: claim_task's "roles" argument must
// come from the FULL parsed AGENT_ROLE set (internal/agentrole), not a
// single hardcoded role — that hardcoding is exactly what deadlocked a
// mission on the first role a small herd didn't staff (#23/#39). Runs the
// queue loop against a stubbed brain that always reports an empty queue
// (task:null) so the loop never leaves claim_task/heartbeat; only the FIRST
// claim_task call's args are asserted.
func TestRunQueueLoopBuildsClaimArgsFromRoleSet(t *testing.T) {
	cases := []struct {
		name      string
		agentRole string
		want      []string
	}{
		{"single role unchanged", "builder", []string{"builder"}},
		{"comma list claims the whole set", "researcher, designer,tester", []string{"researcher", "designer", "tester"}},
		{"any is a generalist (empty filter)", "any", []string{}},
		{"empty is a generalist (empty filter)", "", []string{}},
		{"star is a generalist (empty filter)", "*", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A long poll interval keeps the (deliberately leaked — runQueueLoop
			// has no cancellation path) background goroutine quiet for the rest
			// of the test binary's life instead of spinning.
			t.Setenv("AGENT_POLL_SECONDS", "3600")
			rs := agentrole.Parse(c.agentRole)

			captured := make(chan []string, 1)
			brain := func(tool string, args map[string]any) string {
				if tool == "claim_task" {
					roles, _ := args["roles"].([]string)
					select {
					case captured <- roles:
					default:
					}
					return `{"task":null}`
				}
				return "{}"
			}

			go runQueueLoop(context.Background(), nil, "Test", rs.Display(), rs, t.TempDir(), brain, nil)

			select {
			case got := <-captured:
				if !reflect.DeepEqual(got, c.want) {
					t.Fatalf("claim_task roles = %#v, want %#v", got, c.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("claim_task was never called")
			}
		})
	}
}
