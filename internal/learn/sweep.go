// SPDX-License-Identifier: Elastic-2.0

package learn

import (
	"strings"
)

// minOccurrences is the recurrence threshold: a finding signature (or lesson
// cluster) must be seen at least this many times before it becomes a
// proposal worth a human's attention.
const minOccurrences = 3

// jaccardThreshold is the minimum token-Jaccard similarity for two lesson
// docs to be linked into the same cluster.
const jaccardThreshold = 0.5

// FindingSignal is one observed occurrence of a recurring problem — e.g. a
// missing requirement or a repeated bug — surfaced by a herd role during a
// run.
type FindingSignal struct {
	Type     string
	Target   string
	Role     string
	Evidence string
}

// LessonDoc is a candidate lesson document (name + body) considered for
// near-duplicate clustering.
type LessonDoc struct {
	Name   string
	Body   string
	Author string
}

// Signature is the dedup key for a finding: its type and target.
func Signature(f FindingSignal) string {
	return f.Type + "|" + f.Target
}

// tokenize lowercases s, splits on runs of non-alphanumeric characters, and
// drops tokens shorter than 3 characters.
func tokenize(s string) map[string]struct{} {
	set := make(map[string]struct{})
	var b strings.Builder
	flush := func() {
		if b.Len() >= 3 {
			set[b.String()] = struct{}{}
		}
		b.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return set
}

// jaccard computes token-overlap similarity between two token sets: |a∩b|
// over the smaller set's size (the Szymkiewicz-Simpson overlap coefficient).
// Near-duplicate lessons tend to differ in length (one restates the other
// with extra words), which sinks a strict |a∩b|/|a∪b| Jaccard score well
// below any reasonable threshold; the overlap coefficient stays high as
// long as the shorter doc's tokens are (almost) a subset of the longer's,
// which is exactly the near-duplicate shape this clustering targets.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	return float64(inter) / float64(min)
}

// unionFind is a minimal disjoint-set structure for clustering.
type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p}
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// ClusterLessons groups near-duplicate lesson docs: two docs are linked when
// their body token-overlap similarity is ≥ 0.5, and connected groups whose
// size is ≥ minSize are emitted as clusters. This is the deliberate v1 —
// pure-Go token overlap, no FTS or vector search (spec-deferred upgrades).
func ClusterLessons(docs []LessonDoc, minSize int) [][]LessonDoc {
	n := len(docs)
	if n == 0 {
		return nil
	}
	tokens := make([]map[string]struct{}, n)
	for i, d := range docs {
		tokens[i] = tokenize(d.Body)
	}
	uf := newUnionFind(n)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if jaccard(tokens[i], tokens[j]) >= jaccardThreshold {
				uf.union(i, j)
			}
		}
	}
	groups := make(map[int][]LessonDoc)
	order := make([]int, 0, n)
	for i := 0; i < n; i++ {
		r := uf.find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], docs[i])
	}
	var out [][]LessonDoc
	for _, r := range order {
		if len(groups[r]) >= minSize {
			out = append(out, groups[r])
		}
	}
	return out
}

// slugify lowercases s and replaces runs of non-alphanumeric characters with
// a single "-".
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// Sweep is the deterministic, LLM-free detection pass: it groups findings by
// Signature and clusters lessons by near-duplicate similarity, opening a new
// pending proposal (via Upsert) for each group/cluster that meets the
// minOccurrences threshold. It returns only the proposals that were newly
// created (created=true) — re-sweeping the same input bumps existing
// pending rows instead of opening duplicates.
func (s *Store) Sweep(findings []FindingSignal, lessons []LessonDoc) ([]Proposal, error) {
	var opened []Proposal

	type findingGroup struct {
		role     string
		evidence []string
	}
	groups := make(map[string]*findingGroup)
	var sigOrder []string
	for _, f := range findings {
		sig := Signature(f)
		g, ok := groups[sig]
		if !ok {
			g = &findingGroup{role: f.Role}
			groups[sig] = g
			sigOrder = append(sigOrder, sig)
		}
		g.evidence = append(g.evidence, f.Evidence)
	}
	for _, sig := range sigOrder {
		g := groups[sig]
		if len(g.evidence) < minOccurrences {
			continue
		}
		p, created, err := s.Upsert(sig, "finding", g.role, g.evidence)
		if err != nil {
			return opened, err
		}
		if created {
			opened = append(opened, *p)
		}
	}

	for _, cluster := range ClusterLessons(lessons, minOccurrences) {
		sig := "lesson|" + slugify(cluster[0].Name)
		evidence := make([]string, 0, len(cluster)*2)
		for _, d := range cluster {
			evidence = append(evidence, d.Name, d.Body)
		}
		p, created, err := s.Upsert(sig, "lesson", "", evidence)
		if err != nil {
			return opened, err
		}
		if created {
			opened = append(opened, *p)
		}
	}

	return opened, nil
}
