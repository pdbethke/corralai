// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"context"
	"strings"
)

// ChangedFiles lists the files touched by the HEAD commit (added/modified).
func (e *Engine) ChangedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := e.git(ctx, dir, "show", "--name-only", "--pretty=format:", "HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// DiffAddedLines returns the raw patch text for EVERY commit between base and
// HEAD (`git log -p base..HEAD`), so the egress gate can scan the added lines
// of the full branch history — not just the net diff. A secret committed in an
// earlier phase and then deleted (clean final tree) has no net effect and is
// invisible to ChangedFilesRange (which diffs base...HEAD) or a squash, but the
// push ships the whole history, so the secret still leaves. Scanning every
// commit's added lines is the only correct detector. `--unified=0` drops
// context lines (only real adds/removes remain); `--no-color` keeps the text
// machine-parseable. The output is redact()ed like all e.git output, which only
// masks the configured forge token, never a planted secret.
//
// `--diff-merges=first-parent` is REQUIRED: `git log -p` omits merge-commit
// diffs by default, so a secret smuggled in only via a merge's conflict
// resolution (text present in neither parent — an "evil merge") would
// otherwise never be scanned even though it ships to the remote. Showing the
// first-parent diff of each merge surfaces exactly that resolution content.
func (e *Engine) DiffAddedLines(ctx context.Context, dir, base string) (string, error) {
	return e.git(ctx, dir, "log", "-p", "--no-color", "--unified=0", "--diff-merges=first-parent", base+"..HEAD")
}

// ChangedFilesRange lists files that differ between base and HEAD — the
// mission's cumulative diff across every phase commit, not just the most
// recent one. The egress-scan gate uses this (rather than ChangedFiles) so a
// secret committed in an earlier phase is still caught at push time.
func (e *Engine) ChangedFilesRange(ctx context.Context, dir, base string) ([]string, error) {
	out, err := e.git(ctx, dir, "diff", "--name-only", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}
