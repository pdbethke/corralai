package reposcan

import (
	"encoding/json"
	"os"
	"strings"
)

// Goal is the correctness/security property a file's tests are graded
// against. Provenance travels with it into the report so a reader can see
// where the goal came from and judge whether it was the right one.
type Goal struct {
	Text       string
	Provenance string
}

// Goal provenance values.
const (
	GoalFromFile = "file"
)

// GoalSource supplies the goal for a candidate. ok=false means UNGOALED:
// the file drops out of the scored surface and is accounted for in the
// report. Implementations must NEVER invent a goal to avoid returning false —
// a wrong goal audits the wrong thing, silently.
type GoalSource interface {
	GoalFor(c Candidate) (Goal, bool, error)
}

type fileGoalSource struct{ goals map[string]string }

// NewFileGoalSource reads a JSON object mapping repo-relative paths to goal
// text. This is the H1a stand-in for LLM derivation (H1b), which will
// implement the same interface.
func NewFileGoalSource(path string) (GoalSource, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return fileGoalSource{goals: m}, nil
}

func (f fileGoalSource) GoalFor(c Candidate) (Goal, bool, error) {
	text := strings.TrimSpace(f.goals[c.Path])
	if text == "" {
		return Goal{}, false, nil
	}
	return Goal{Text: text, Provenance: GoalFromFile}, true, nil
}
