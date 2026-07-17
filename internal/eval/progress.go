// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// Progress is a git-ignored record of completed (corpus_version, target, iter)
// so a re-run tops up rather than restarting. Persisted after each mark.
type Progress struct {
	path string
	Done map[string]bool `json:"done"` // key = corpusVersion + "|" + targetID + "|" + iter
}

func progKey(corpusVersion, targetID string, iter int) string {
	return fmt.Sprintf("%s|%s|%d", corpusVersion, targetID, iter)
}

func loadProgress(path string) (*Progress, error) {
	p := &Progress{path: path, Done: map[string]bool{}}
	raw, err := os.ReadFile(path) // #nosec G304
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("eval: read progress: %w", err)
	}
	if err := json.Unmarshal(raw, p); err != nil || p.Done == nil {
		// Corrupt/empty progress → start fresh rather than fail the run.
		p.Done = map[string]bool{}
	}
	return p, nil
}

func (p *Progress) done(corpusVersion, targetID string, iter int) bool {
	return p.Done[progKey(corpusVersion, targetID, iter)]
}

func (p *Progress) mark(corpusVersion, targetID string, iter int) error {
	p.Done[progKey(corpusVersion, targetID, iter)] = true
	raw, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(p.path, raw, 0o600) // local resumable state; least-privilege (gosec G306)
}
