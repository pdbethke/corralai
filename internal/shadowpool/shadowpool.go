// SPDX-License-Identifier: Elastic-2.0

// Package shadowpool draws a CHALLENGER seat's model from an operator-named
// pool by Thompson sampling (docs/design/shadow-seat-selection.md). It is
// pure: no store, no flags, no advpool — history is handed in, and the
// caller writes the chosen member back into the seat's ordinary flag so
// every existing rule applies to it unchanged.
//
// It never touches a seat that gates a verdict. That is option (a), the
// founder's call; moving statistics into a verdict seat is option (b) and
// needs its own decision.
package shadowpool

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
)

const (
	HistoryRead    = "read"
	HistoryNotRead = "not read"
	// LangAny is the language a pooled (cross-language) draw records.
	LangAny = "any"
)

type Counts struct{ Successes, Trials int }

type Candidate struct{ Typed, Concrete string }

type Member struct {
	Model    string  `json:"model"`
	Concrete string  `json:"concrete,omitempty"`
	Alpha    float64 `json:"alpha"`
	Beta     float64 `json:"beta"`
	Sample   float64 `json:"sample"`
}

type Selection struct {
	Role        string   `json:"role"`
	Lang        string   `json:"lang"`
	Members     []Member `json:"members"`
	Chosen      string   `json:"chosen"`
	Seed        string   `json:"seed"`
	History     string   `json:"history"`
	HistoryRows int      `json:"history_rows"`
}

// ParsePool splits a comma-separated pool. It refuses an empty member, a
// duplicate (case-insensitive), "off", and a pool of fewer than two — one
// model is a NAMED seat, and belongs in the plain model flag.
func ParsePool(flagName, value string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(value, ",") {
		m := strings.TrimSpace(raw)
		switch {
		case m == "":
			return nil, fmt.Errorf("--%s %q has an empty member", flagName, value)
		case strings.EqualFold(m, "off"):
			return nil, fmt.Errorf("--%s cannot contain \"off\": leave the flag out to disable the seat", flagName)
		case seen[strings.ToLower(m)]:
			return nil, fmt.Errorf("--%s names %q twice", flagName, m)
		}
		seen[strings.ToLower(m)] = true
		out = append(out, m)
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("--%s needs at least two models; to name one challenger, use the seat's plain model flag", flagName)
	}
	return out, nil
}

// Draw samples Beta(1+successes, 1+failures) per candidate and picks the
// largest sample; ties go to the earlier candidate. history is keyed by
// CONCRETE model name, because that is what the scorecard records.
func Draw(role, lang string, cands []Candidate, history map[string]Counts, historyRead bool, rows int, seed uint64) Selection {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) // #nosec G404 -- not security: a reproducible draw from a recorded seed
	sel := Selection{Role: role, Lang: lang, Seed: FormatSeed(seed), History: HistoryNotRead}
	if historyRead {
		sel.History, sel.HistoryRows = HistoryRead, rows
	}
	best := -1.0
	for _, c := range cands {
		h := history[c.Concrete]
		fail := h.Trials - h.Successes
		if fail < 0 {
			fail = 0
		}
		m := Member{Model: c.Typed, Alpha: 1 + float64(h.Successes), Beta: 1 + float64(fail)}
		if c.Concrete != c.Typed {
			m.Concrete = c.Concrete
		}
		m.Sample = betaSample(rng, m.Alpha, m.Beta)
		if m.Sample > best {
			best, sel.Chosen = m.Sample, c.Typed
		}
		sel.Members = append(sel.Members, m)
	}
	return sel
}

// Drawn returns the selection for role, if that seat was drawn.
func Drawn(sels []Selection, role string) (Selection, bool) {
	for _, s := range sels {
		if s.Role == role {
			return s, true
		}
	}
	return Selection{}, false
}

func FormatSeed(seed uint64) string { return fmt.Sprintf("0x%016x", seed) }

func ParseSeed(s string) (uint64, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(s), "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("--shadow-seed %q is not a hex seed (the record prints it as 0x…)", s)
	}
	return v, nil
}

// betaSample draws Beta(a, b) as X/(X+Y) with X~Gamma(a), Y~Gamma(b).
func betaSample(rng *rand.Rand, a, b float64) float64 {
	x, y := gammaSample(rng, a), gammaSample(rng, b)
	return x / (x + y)
}

// gammaSample is Marsaglia–Tsang, valid for shape >= 1 — always true here,
// since every posterior parameter is 1 + a non-negative count.
func gammaSample(rng *rand.Rand, k float64) float64 {
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
