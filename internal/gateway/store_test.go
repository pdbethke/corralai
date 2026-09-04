// SPDX-License-Identifier: Elastic-2.0

package gateway

import (
	"path/filepath"
	"testing"
)

func TestGovernance(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "g.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	reg := func(name, owner, tok string) error {
		return s.Register(Endpoint{Name: name, Endpoint: "http://x", Transport: "http", Enabled: true},
			Auth{Header: "Authorization", Token: tok}, owner)
	}
	usable := func(p string) int { u, _ := s.Usable(p); return len(u) }

	// A registers a personal endpoint — usable only by A.
	if err := reg("gh", "a@x.com", "t1"); err != nil {
		t.Fatal(err)
	}
	if usable("a@x.com") != 1 {
		t.Fatal("owner A must see their own endpoint")
	}
	if usable("b@x.com") != 0 {
		t.Fatal("B must NOT see A's personal endpoint")
	}

	// B cannot clobber A's name.
	if err := reg("gh", "b@x.com", "evil"); err == nil {
		t.Fatal("B clobbered A's endpoint name")
	}

	// Resolve hands auth to A only.
	if _, auth, ok, _ := s.Resolve("gh", "a@x.com"); !ok || auth.Token != "t1" {
		t.Fatal("A must resolve with its secret")
	}
	if _, _, ok, _ := s.Resolve("gh", "b@x.com"); ok {
		t.Fatal("B must not resolve A's personal endpoint")
	}

	// Admin promotes to public (everyone).
	if ok, _ := s.Promote("gh", true, nil, Auth{}); !ok {
		t.Fatal("promote failed")
	}
	if usable("b@x.com") != 1 {
		t.Fatal("B must see the now-public endpoint")
	}

	// Scope to C only — B excluded, owner A still allowed.
	s.Promote("gh", true, []string{"c@x.com"}, Auth{})
	if usable("b@x.com") != 0 {
		t.Fatal("B must be excluded by scope")
	}
	if usable("c@x.com") != 1 {
		t.Fatal("C must be included by scope")
	}
	if usable("a@x.com") != 1 {
		t.Fatal("owner A must remain usable regardless of scope")
	}

	// Promote with a team token swaps the secret for permitted users.
	s.Promote("gh", true, nil, Auth{Header: "Authorization", Token: "team"})
	if _, auth, ok, _ := s.Resolve("gh", "b@x.com"); !ok || auth.Token != "team" {
		t.Fatal("team credential must replace the owner's secret on promote")
	}

	// Disable hides it from everyone (incl. owner).
	s.SetEnabled("gh", false)
	if usable("a@x.com") != 0 {
		t.Fatal("disabled endpoint must be hidden")
	}
}

// TestPromotedEndpointIsFrozenToItsOwner pins the sixth review's #6: after
// an admin promoted an endpoint public under a TEAM credential, the owner
// could Register the same name again with a new URL and an empty token —
// keeping the team secret and pointing it at a host of their own. Every
// teammate's call then carried Authorization: TEAM-SECRET to that host.
func TestPromotedEndpointIsFrozenToItsOwner(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "g.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg := func(url, tok string) error {
		return s.Register(Endpoint{Name: "shared", Endpoint: url, Transport: "http", Enabled: true},
			Auth{Header: "Authorization", Token: tok}, "alice@x.com")
	}
	if err := reg("https://good.example/mcp", "alice-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote("shared", true, nil, Auth{Header: "Authorization", Token: "TEAM-SECRET"}); err != nil {
		t.Fatal(err)
	}
	if err := reg("https://alice-evil.example/mcp", ""); err == nil {
		t.Fatal("the owner re-pointed a promoted endpoint's URL")
	}
	ep, auth, ok, _ := s.Resolve("shared", "bob@x.com")
	if !ok || ep.Endpoint != "https://good.example/mcp" || auth.Token != "TEAM-SECRET" {
		t.Fatalf("bob resolves to %q with %q, want the promoted URL and the team secret", ep.Endpoint, auth.Token)
	}
	// Demoted, the owner may edit again.
	if _, err := s.Promote("shared", false, nil, Auth{}); err != nil {
		t.Fatal(err)
	}
	if err := reg("https://new.example/mcp", ""); err != nil {
		t.Fatalf("a private endpoint's owner could not edit it: %v", err)
	}
}
