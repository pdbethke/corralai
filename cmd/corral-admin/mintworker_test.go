// SPDX-License-Identifier: Elastic-2.0
package main

import "testing"

func TestWorkerSpawnArgs(t *testing.T) {
	// out_of_process must ALWAYS be true — without it spawn_subagent mints no
	// delegation token and the worker has nothing to authenticate with.
	got := workerSpawnArgs("tester", "pod-tester", 604800)
	if got["out_of_process"] != true {
		t.Fatalf("out_of_process = %v, want true (no token minted otherwise)", got["out_of_process"])
	}
	if got["role"] != "tester" {
		t.Errorf("role = %v, want tester", got["role"])
	}
	if got["name"] != "pod-tester" {
		t.Errorf("name = %v, want pod-tester", got["name"])
	}
	if got["ttl_seconds"] != 604800 {
		t.Errorf("ttl_seconds = %v, want 604800", got["ttl_seconds"])
	}
}

func TestWorkerSpawnArgsDefaultName(t *testing.T) {
	// An empty name derives a stable, role-scoped default so the operator can
	// mint with just --role.
	got := workerSpawnArgs("builder", "", 3600)
	if got["name"] != "pod-builder" {
		t.Errorf("default name = %v, want pod-builder", got["name"])
	}
}
