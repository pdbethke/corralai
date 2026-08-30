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
	// goalDerivedPrefix opens every Provenance a derivingGoalSource writes
	// (see derive.go's "derived:%s@%s") — the one thing that distinguishes a
	// machine-derived goal from a hand-written one after the job is built.
	goalDerivedPrefix = "derived:"
)

// GoalWasDerived reports whether g came from a model (derivingGoalSource),
// as opposed to a hand-written --goals map. Used to decide
// scanstore.File.GoalsDerived: that column counts goals the DERIVER
// produced, and a hand-written goal was never asked of a deriver at all.
func GoalWasDerived(g Goal) bool {
	return strings.HasPrefix(g.Provenance, goalDerivedPrefix)
}

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
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied --goals path, same trust class as --code
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
