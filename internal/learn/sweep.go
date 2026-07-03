// SPDX-License-Identifier: Elastic-2.0

package learn

import (
	"strings"
)

// minOccurrences is the recurrence threshold: a finding signature (or lesson
// cluster) must be seen at least this many times before it becomes a
// proposal worth a human's attention.
const minOccurrences = 3

// overlapThreshold is the minimum token-overlap coefficient for two lesson
// docs to be linked into the same cluster.
const overlapThreshold = 0.5

// minSharedTokens is the absolute floor on |a∩b| for a link. The overlap
// coefficient is monotonically ≥ Jaccard, so the 0.5 threshold alone is more
// permissive than the spec's Jaccard-calibrated 0.5: a very short body whose
// few kept tokens happen to appear in an unrelated longer doc scores high on
// ratio despite sharing almost nothing (e.g. 2 of 3 tokens ≈ 0.67).
// Calibration: genuinely restated lessons in the canonical fixture share 5-6
// tokens per linking pair, while the short-vs-long noise shape shares 2-3 —
// so 4 cleanly separates the two.
const minSharedTokens = 4

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

// linked reports whether two token sets are similar enough to join a
// cluster: the Szymkiewicz-Simpson overlap coefficient (|a∩b| over the
// smaller set's size) must reach overlapThreshold, AND the raw intersection
// must reach minSharedTokens. Near-duplicate lessons tend to differ in
// length (one restates the other with extra words), which sinks a strict
// |a∩b|/|a∪b| Jaccard score well below any reasonable threshold; the
// overlap coefficient stays high for that shape, and the shared-token floor
// keeps it from also linking a tiny body to any long doc that happens to
// contain its few tokens.
func linked(a, b map[string]struct{}) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	if inter < minSharedTokens {
		return false
	}
	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	return float64(inter)/float64(smaller) >= overlapThreshold
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
// their body token-overlap coefficient is ≥ 0.5 and they share at least
// minSharedTokens tokens; connected groups of size ≥ minSize are emitted as
// clusters. This is the deliberate v1 — pure-Go token overlap, no FTS or
// vector search (spec-deferred upgrades).
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
			if linked(tokens[i], tokens[j]) {
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
		roles    []string // distinct, first-seen order
		roleSeen map[string]struct{}
		evidence []string
	}
	groups := make(map[string]*findingGroup)
	var sigOrder []string
	for _, f := range findings {
		sig := Signature(f)
		g, ok := groups[sig]
		if !ok {
			g = &findingGroup{roleSeen: make(map[string]struct{})}
			groups[sig] = g
			sigOrder = append(sigOrder, sig)
		}
		if _, seen := g.roleSeen[f.Role]; !seen && f.Role != "" {
			g.roleSeen[f.Role] = struct{}{}
			g.roles = append(g.roles, f.Role)
		}
		g.evidence = append(g.evidence, f.Evidence)
	}
	for _, sig := range sigOrder {
		g := groups[sig]
		if len(g.evidence) < minOccurrences {
			continue
		}
		p, created, err := s.Upsert(sig, "finding", strings.Join(g.roles, ","), g.evidence)
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
