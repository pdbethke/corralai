// SPDX-License-Identifier: Elastic-2.0
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// workerSpawnArgs builds the spawn_subagent arguments for a detached pod/floor
// worker. out_of_process is ALWAYS true — that is what makes the brain mint a
// delegation token; without it the worker has no credential. An empty name
// derives a stable role-scoped default so the operator can mint with just
// --role.
func workerSpawnArgs(role, name string, ttlSeconds int) map[string]any {
	if name == "" {
		name = "pod-" + role
	}
	return map[string]any{
		"name":           name,
		"role":           role,
		"out_of_process": true,
		"ttl_seconds":    ttlSeconds,
	}
}

// cmdMintWorker mints a delegation token for a detached frontier worker (the
// pod/floor pool). The token is TTL-bound and human-gate-refused for admin
// writes — a worker can pass tasks but cannot vet its own skills/memory.
func cmdMintWorker(args []string) {
	fs := flag.NewFlagSet("mint-worker", flag.ExitOnError)
	c := bind(fs)
	role := fs.String("role", "builder", "role the worker serves (e.g. builder, tester)")
	name := fs.String("name", "", "worker name (default: pod-<role>)")
	ttl := fs.Duration("ttl", 168*time.Hour, "token lifetime (e.g. 168h for a week)")
	parseFlags(fs, args)
	c.do("spawn_subagent", workerSpawnArgs(*role, *name, int(ttl.Seconds())), func(out json.RawMessage) {
		var r struct {
			Name    string `json:"name"`
			Token   string `json:"token"`
			Model   string `json:"model"`
			Backend string `json:"backend"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(out, &r)
		if r.Error != "" {
			fatal(r.Error)
		}
		if r.Token == "" {
			fatal("brain returned no token (is delegation enabled on this brain?)")
		}
		fmt.Println(r.Token) // token to stdout, so it pipes cleanly into a file
		info := "minted worker " + r.Name
		if r.Model != "" {
			info += " (" + r.Backend + ":" + r.Model + ")"
		}
		fmt.Fprintln(os.Stderr, info)
	})
}
