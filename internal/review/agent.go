// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"fmt"
	"strings"
)

// AgentBrief is the user turn for an AGENTIC seat — a coding CLI (Claude
// Code, Codex) that can read the repository itself. It is handed a
// disposable copy of the tree as its working directory and the scope's
// file list, not the files' contents: it reads what it needs, beyond any
// byte cap. The contract does not move: it hands back scripts, corral
// runs them, and nothing the agent ran itself is on the record — its
// tools are read-only, and the brief says so.
func AgentBrief(repo, commit, scope string, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nCommit: %s\nScope: %s\n\n", repo, commit, scope)
	b.WriteString("You are running inside a DISPOSABLE COPY of the repository at this commit: your working directory is that copy. Read any file you need with your file tools; the scope is the list below, and you may look outside it to understand it. Your tools are read-only on purpose: you cannot run tests or commands, and you must not try — every REPRODUCED finding is a sh SCRIPT you hand back, which corral runs in another copy of the tree and records; nothing you execute yourself counts. Return the ONE JSON object the instructions describe, and nothing else.\n\n")
	fmt.Fprintf(&b, "The scope, %d file(s):\n", len(files))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	return b.String()
}
