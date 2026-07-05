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
