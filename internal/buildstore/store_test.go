// SPDX-License-Identifier: Elastic-2.0

package buildstore

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	stmt := `{"predicateType":"https://slsa.dev/provenance/v1","subject":[{"name":"corral"}]}`
	steps := `[{"seq":0,"kind":"context","hash":"abc"}]`
	rekor := `{"log_index":42,"log_id":"rekor-log"}`
	commitSig := `{"signed":true,"verified":"good","signer":"Peter <peter@example.com>","mechanism":"gpg"}`
	id, err := s.Save("pdbethke/corralai", "abc123", "feat/x", "peter", "deadbeef", "sig-bytes-hex", stmt, steps, rekor, true,
		"fix the thing", "Peter <peter@example.com>", "2026-07-09T12:00:00-05:00", commitSig, true)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero id")
	}

	got, ok, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected found=true for a saved id")
	}
	if got["predicateType"] != "https://slsa.dev/provenance/v1" {
		t.Fatalf("statement not round-tripped correctly: %v", got)
	}
	if got["anchored"] != true {
		t.Fatalf("expected anchored=true round-tripped, got %v (%T)", got["anchored"], got["anchored"])
	}
	rekorStr, ok := got["rekor"].(string)
	if !ok || rekorStr == "" {
		t.Fatalf("expected a non-empty rekor string, got %v (%T)", got["rekor"], got["rekor"])
	}
	var rekorMap map[string]any
	if err := json.Unmarshal([]byte(rekorStr), &rekorMap); err != nil {
		t.Fatalf("stored rekor is not valid JSON: %v", err)
	}
	if rekorMap["log_id"] != "rekor-log" {
		t.Fatalf("stored rekor content mismatch: %v", rekorMap)
	}
	rawSteps, ok := got["steps"]
	if !ok {
		t.Fatal("expected a \"steps\" key in the returned map")
	}
	stepsList, ok := rawSteps.([]any)
	if !ok || len(stepsList) != 1 {
		t.Fatalf("steps not round-tripped correctly: %v (%T)", rawSteps, rawSteps)
	}
	stepObj, ok := stepsList[0].(map[string]any)
	if !ok || stepObj["kind"] != "context" {
		t.Fatalf("steps[0] not round-tripped correctly: %v", stepsList[0])
	}

	if got["commit_message"] != "fix the thing" {
		t.Fatalf("commit_message not round-tripped correctly: %v", got["commit_message"])
	}
	if got["commit_author"] != "Peter <peter@example.com>" {
		t.Fatalf("commit_author not round-tripped correctly: %v", got["commit_author"])
	}
	if got["commit_date"] != "2026-07-09T12:00:00-05:00" {
		t.Fatalf("commit_date not round-tripped correctly: %v", got["commit_date"])
	}
	commitSigStr, ok := got["commit_signature"].(string)
	if !ok || commitSigStr == "" {
		t.Fatalf("expected a non-empty commit_signature string, got %v (%T)", got["commit_signature"], got["commit_signature"])
	}
	var commitSigMap map[string]any
	if err := json.Unmarshal([]byte(commitSigStr), &commitSigMap); err != nil {
		t.Fatalf("stored commit_signature is not valid JSON: %v", err)
	}
	if commitSigMap["verified"] != "good" {
		t.Fatalf("stored commit_signature content mismatch: %v", commitSigMap)
	}
	if got["pass"] != true {
		t.Fatalf("expected pass=true round-tripped, got %v (%T)", got["pass"], got["pass"])
	}

	// Absent id.
	_, ok, err = s.Get(id + 999)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected found=false for an absent id")
	}
}

func TestSaveAssignsIncreasingIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id1, err := s.Save("r", "c1", "b", "a", "h1", "sig1", `{"n":1}`, `[]`, "", false, "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Save("r", "c2", "b", "a", "h2", "sig2", `{"n":2}`, `[]`, "", false, "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if id2 <= id1 {
		t.Fatalf("expected increasing ids, got id1=%d id2=%d", id1, id2)
	}
	// Empty commit_signature must round-trip as "" (unavailable git context),
	// not as an error or a literal "null" string.
	got, ok, err := s.Get(id1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got["commit_signature"] != "" {
		t.Fatalf("expected empty commit_signature for an unset value, got %v", got["commit_signature"])
	}
	if got["pass"] != false {
		t.Fatalf("expected pass=false, got %v", got["pass"])
	}
}

func TestLoadOrCreateSigningKeyPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "certify.key")

	priv1, err := LoadOrCreateSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(priv1) != ed25519.PrivateKeySize {
		t.Fatalf("unexpected key size %d", len(priv1))
	}

	// Seed file must be 0600.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("seed file mode = %v, want 0600", fi.Mode().Perm())
	}

	priv2, err := LoadOrCreateSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv1, priv2) {
		t.Fatal("reloading from the same path must return the byte-identical key")
	}
}

// statementWithProducers builds a minimal SLSA-shaped statement JSON whose
// predicate.buildDefinition.resolvedDependencies carries the given model
// URIs, matching internal/certify.BuildAttestation's shape.
func statementWithProducers(models ...string) string {
	deps := make([]map[string]any, 0, len(models))
	for _, m := range models {
		deps = append(deps, map[string]any{"uri": "model:" + m})
	}
	stmt := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": map[string]any{
			"buildDefinition": map[string]any{
				"resolvedDependencies": deps,
			},
		},
	}
	b, err := json.Marshal(stmt)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Control created_ts so ordering/Since/Until are deterministic instead of
	// racing wall-clock `now`.
	orig := now
	defer func() { now = orig }()
	var ts float64 = 1000
	now = func() float64 {
		v := ts
		ts += 10
		return v
	}

	signedSig := `{"signed":true,"verified":"good","signer":"Peter","mechanism":"gpg"}`
	unsignedSig := `{"signed":false}`

	// id1: ts=1000, repoA, alice, pass, anchored, signed, produced_by [claude-opus]
	id1, err := s.Save("repoA", "c1", "main", "alice", "h1", "sig1",
		statementWithProducers("claude-opus"), "[]", "", true,
		"m1", "alice", "d1", signedSig, true)
	if err != nil {
		t.Fatal(err)
	}
	// id2: ts=1010, repoA, bob, fail, not anchored, unsigned, no produced_by
	id2, err := s.Save("repoA", "c2", "main", "bob", "h2", "sig2",
		statementWithProducers(), "[]", "", false,
		"m2", "bob", "d2", unsignedSig, false)
	if err != nil {
		t.Fatal(err)
	}
	// id3: ts=1020, repoB, alice, pass, anchored, no commit_signature (empty)
	id3, err := s.Save("repoB", "c3", "main", "alice", "h3", "sig3",
		statementWithProducers("claude-sonnet", "gpt-5"), "[]", "", true,
		"m3", "alice", "d3", "", true)
	if err != nil {
		t.Fatal(err)
	}
	// id4: ts=1030, repoA, alice, fail, anchored, signed
	id4, err := s.Save("repoA", "c4", "main", "alice", "h4", "sig4",
		statementWithProducers(), "[]", "", true,
		"m4", "alice", "d4", signedSig, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = id2
	_ = id4

	// Empty table (fresh store) → empty slice, no error.
	emptyStore, err := Open(filepath.Join(dir, "empty.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer emptyStore.Close()
	got, err := emptyStore.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice for empty table, got %v", got)
	}

	// No filter: all 4, newest-first (id4, id3, id2, id1).
	all, err := s.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 records, got %d", len(all))
	}
	wantOrder := []int64{id4, id3, id2, id1}
	for i, w := range wantOrder {
		if all[i].ID != w {
			t.Fatalf("order[%d]: want id %d, got %d (full: %+v)", i, w, all[i].ID, all)
		}
	}
	for i := 1; i < len(all); i++ {
		if all[i].CreatedTS > all[i-1].CreatedTS {
			t.Fatalf("expected DESC created_ts ordering, got %v then %v", all[i-1].CreatedTS, all[i].CreatedTS)
		}
	}

	// Summary field population, checked against id1.
	var s1 *Summary
	for i := range all {
		if all[i].ID == id1 {
			s1 = &all[i]
		}
	}
	if s1 == nil {
		t.Fatal("id1 missing from List results")
	}
	if s1.Repo != "repoA" || s1.Commit != "c1" || s1.Branch != "main" || s1.Actor != "alice" {
		t.Fatalf("id1 fields mismatch: %+v", s1)
	}
	if !s1.Pass {
		t.Fatalf("id1 expected Pass=true, got %+v", s1)
	}
	if !s1.Anchored {
		t.Fatalf("id1 expected Anchored=true, got %+v", s1)
	}
	if !s1.CommitSigned {
		t.Fatalf("id1 expected CommitSigned=true, got %+v", s1)
	}
	if len(s1.ProducedBy) != 1 || s1.ProducedBy[0] != "claude-opus" {
		t.Fatalf("id1 expected ProducedBy [claude-opus], got %+v", s1.ProducedBy)
	}
	if s1.CreatedTS != 1000 {
		t.Fatalf("id1 expected CreatedTS 1000, got %v", s1.CreatedTS)
	}

	// id2: unsigned commit, no produced_by, not anchored, fail.
	var s2 *Summary
	for i := range all {
		if all[i].ID == id2 {
			s2 = &all[i]
		}
	}
	if s2 == nil {
		t.Fatal("id2 missing from List results")
	}
	if s2.Pass {
		t.Fatalf("id2 expected Pass=false, got %+v", s2)
	}
	if s2.Anchored {
		t.Fatalf("id2 expected Anchored=false, got %+v", s2)
	}
	if s2.CommitSigned {
		t.Fatalf("id2 expected CommitSigned=false, got %+v", s2)
	}
	if len(s2.ProducedBy) != 0 {
		t.Fatalf("id2 expected empty ProducedBy, got %+v", s2.ProducedBy)
	}

	// id3: empty commit_signature (NULL) → CommitSigned=false, no error; two producers.
	var s3 *Summary
	for i := range all {
		if all[i].ID == id3 {
			s3 = &all[i]
		}
	}
	if s3 == nil {
		t.Fatal("id3 missing from List results")
	}
	if s3.CommitSigned {
		t.Fatalf("id3 expected CommitSigned=false (no commit_signature), got %+v", s3)
	}
	if len(s3.ProducedBy) != 2 || s3.ProducedBy[0] != "claude-sonnet" || s3.ProducedBy[1] != "gpt-5" {
		t.Fatalf("id3 expected ProducedBy [claude-sonnet gpt-5], got %+v", s3.ProducedBy)
	}

	// Filter: Repo exact match.
	byRepo, err := s.List(ListFilter{Repo: "repoB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRepo) != 1 || byRepo[0].ID != id3 {
		t.Fatalf("Repo filter: expected only id3, got %+v", byRepo)
	}

	// Filter: Actor exact match.
	byActor, err := s.List(ListFilter{Actor: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byActor) != 1 || byActor[0].ID != id2 {
		t.Fatalf("Actor filter: expected only id2, got %+v", byActor)
	}

	// Filter: Status=pass.
	pass, err := s.List(ListFilter{Status: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pass) != 2 {
		t.Fatalf("Status=pass: expected 2 records, got %+v", pass)
	}
	for _, r := range pass {
		if !r.Pass {
			t.Fatalf("Status=pass returned a failing record: %+v", r)
		}
	}

	// Filter: Status=fail.
	fail, err := s.List(ListFilter{Status: "fail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fail) != 2 {
		t.Fatalf("Status=fail: expected 2 records, got %+v", fail)
	}
	for _, r := range fail {
		if r.Pass {
			t.Fatalf("Status=fail returned a passing record: %+v", r)
		}
	}

	// Filter: Anchored true/false.
	anchoredTrue := true
	anchored, err := s.List(ListFilter{Anchored: &anchoredTrue})
	if err != nil {
		t.Fatal(err)
	}
	if len(anchored) != 3 {
		t.Fatalf("Anchored=true: expected 3 records, got %+v", anchored)
	}
	anchoredFalse := false
	notAnchored, err := s.List(ListFilter{Anchored: &anchoredFalse})
	if err != nil {
		t.Fatal(err)
	}
	if len(notAnchored) != 1 || notAnchored[0].ID != id2 {
		t.Fatalf("Anchored=false: expected only id2, got %+v", notAnchored)
	}

	// Filter: Since/Until bounds (inclusive), narrowing to id2+id3 (ts 1010,1020).
	bounded, err := s.List(ListFilter{Since: 1010, Until: 1020})
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) != 2 {
		t.Fatalf("Since/Until: expected 2 records, got %+v", bounded)
	}
	for _, r := range bounded {
		if r.ID != id2 && r.ID != id3 {
			t.Fatalf("Since/Until: unexpected record %+v", r)
		}
	}

	// Pagination: Limit=2 Offset=0 → newest two (id4, id3); Offset=2 → next two (id2, id1).
	page1, err := s.List(ListFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].ID != id4 || page1[1].ID != id3 {
		t.Fatalf("page1: expected [id4 id3], got %+v", page1)
	}
	page2, err := s.List(ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID != id2 || page2[1].ID != id1 {
		t.Fatalf("page2: expected [id2 id1], got %+v", page2)
	}

	// Default Limit (0 → 100) returns everything when under the cap.
	def, err := s.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != 4 {
		t.Fatalf("default limit: expected 4 records, got %d", len(def))
	}
}

func TestLoadOrCreateSigningKeyEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unused.key")

	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	t.Setenv("CORRALAI_CERTIFY_KEY", hex.EncodeToString(seed))

	priv, err := LoadOrCreateSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(priv, want) {
		t.Fatal("env override must produce the key derived from the given seed")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("env override must not write a seed file")
	}
}
