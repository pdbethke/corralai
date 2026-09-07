// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"fmt"
	"strings"
)

// AgentBrief is the user turn for an AGENTIC seat — a coding agent (any
// the operator defines; Claude Code and Codex come defined) that can read
// the repository itself. It is handed a disposable copy of the tree as its
// working directory and the scope's file list, not the files' contents: it
// reads what it needs, beyond any byte cap. The contract does not move: it
// hands back scripts, corral runs them, and nothing the agent ran itself
// is on the record.
func AgentBrief(repo, commit, scope string, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nCommit: %s\nScope: %s\n\n", repo, commit, scope)
	b.WriteString(AgentTreeParagraph("finding"))
	b.WriteString("The scope is the list below; you may look outside it to understand it.\n\n")
	fmt.Fprintf(&b, "The scope, %d file(s):\n", len(files))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	return b.String()
}

// AgentTreeParagraph is the ONE paragraph both agentic briefs (the
// reviewer's and the verifier's) carry about the tree and what an agent may
// do in it. It names how to read — a file reader, or a shell used only to
// read — because an agent whose only file interface IS a shell (Codex) was
// told "you cannot run commands" by an earlier wording and, twice, declined
// to read anything and returned a null review. What is forbidden is
// executing the code under review: tests, builds, the program. What
// reproduces a claim is the script the agent hands back and corral runs.
// kind is "finding" or "refutation".
func AgentTreeParagraph(kind string) string {
	return "You are running inside a DISPOSABLE COPY of the repository at this commit: your working directory is that copy. Read any file you need by whatever your tool provides — a file reader, or a shell used only to read (cat, sed, grep, ls, find). Do NOT run the tests, build, or execute the code under review, and do not modify the copy: nothing you execute yourself counts. Every REPRODUCED " + kind + " is a sh SCRIPT you hand back, which corral runs in another copy of the tree and records. Return the ONE JSON object the instructions describe, and nothing else.\n\n"
}
