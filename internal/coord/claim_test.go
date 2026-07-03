// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"sync"
	"testing"
)

func TestExclusiveRaceExactlyOneWinner(t *testing.T) {
	s := openTmp(t)
	const n = 6
	names := []string{"a", "b", "c", "d", "e", "f"}
	for _, nm := range names {
		if err := s.Register(nm, "", "", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	results := make([]*ClaimResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := s.ClaimPaths(names[i], []string{"src/app.go"}, 3600, true, "")
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			results[i] = r
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, r := range results {
		if r == nil {
			continue
		}
		if len(r.Granted) == 1 && len(r.Conflicts) == 0 {
			winners++
		} else if len(r.Granted) != 0 {
			t.Fatalf("a losing exclusive claim must NOT be granted, got granted=%v conflicts=%v", r.Granted, r.Conflicts)
		}
	}
	if winners != 1 {
		t.Fatalf("want exactly one winner, got %d", winners)
	}
}

func TestSequentialConflictReporting(t *testing.T) {
	s := openTmp(t)
	s.Register("a", "", "", "", "", "")
	s.Register("b", "", "", "", "", "")
	if _, err := s.ClaimPaths("a", []string{"src/app.go"}, 3600, true, "edit"); err != nil {
		t.Fatal(err)
	}
	r, err := s.ClaimPaths("b", []string{"src/app.go"}, 3600, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Granted) != 0 {
		t.Fatalf("b must not be granted, got %v", r.Granted)
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].HeldBy != "a" {
		t.Fatalf("b must see conflict held_by a, got %+v", r.Conflicts)
	}
}

func TestOverlapDirVsFile(t *testing.T) {
	s := openTmp(t)
	s.Register("a", "", "", "", "", "")
	s.Register("b", "", "", "", "", "")
	s.ClaimPaths("a", []string{"src"}, 3600, true, "")
	r, _ := s.ClaimPaths("b", []string{"src/app.go"}, 3600, true, "")
	if len(r.Conflicts) != 1 {
		t.Fatalf("nested path must conflict, got %+v", r.Conflicts)
	}
}

func TestReleaseFreesExclusivePath(t *testing.T) {
	s := openTmp(t)
	s.Register("a", "", "", "", "", "")
	s.Register("b", "", "", "", "", "")
	s.ClaimPaths("a", []string{"src/app.go"}, 3600, true, "")
	if _, err := s.ReleaseClaims("a", []string{"src/app.go"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.ClaimPaths("b", []string{"src/app.go"}, 3600, true, "")
	if len(r.Conflicts) != 0 || len(r.Granted) != 1 {
		t.Fatalf("after release b should claim cleanly, got granted=%v conflicts=%v", r.Granted, r.Conflicts)
	}
}
