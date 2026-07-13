// SPDX-License-Identifier: Elastic-2.0

package auth

import "testing"

func TestPickPrincipal(t *testing.T) {
	cases := []struct {
		name                             string
		email                            string
		emailVerified                    bool
		preferredUsername, clientID, azp string
		want                             string
	}{
		{"verified email wins", "a@x.com", true, "auser", "acli", "aazp", "a@x.com"},
		{"preferred_username when no email", "", false, "auser", "acli", "aazp", "auser"},
		{"client_id namespaced for a service token", "", false, "", "corral-svc", "", "client:corral-svc"},
		{"azp namespaced when only azp present", "", false, "", "", "corral-svc", "client:corral-svc"},
		{"empty when nothing", "", false, "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickPrincipal(c.email, c.emailVerified, c.preferredUsername, c.clientID, c.azp); got != c.want {
				t.Errorf("pickPrincipal(%q,ev=%v,%q,%q,%q) = %q, want %q",
					c.email, c.emailVerified, c.preferredUsername, c.clientID, c.azp, got, c.want)
			}
		})
	}
}

func TestPickPrincipalNamespacesMachineAndGatesEmail(t *testing.T) {
	cases := []struct {
		email              string
		ev                 bool
		pu, cid, azp, want string
	}{
		{"a@b.co", true, "", "", "", "a@b.co"},       // verified email wins, bare
		{"a@b.co", false, "alice", "", "", "alice"},  // unverified email skipped -> preferred_username
		{"", false, "", "svc-1", "", "client:svc-1"}, // machine namespaced
		{"", false, "", "", "azp-9", "client:azp-9"}, // azp namespaced
		{"a@b.co", false, "", "", "", ""},            // only unverified email -> no principal
	}
	for _, c := range cases {
		if got := pickPrincipal(c.email, c.ev, c.pu, c.cid, c.azp); got != c.want {
			t.Errorf("pickPrincipal(%q,ev=%v,%q,%q,%q)=%q want %q", c.email, c.ev, c.pu, c.cid, c.azp, got, c.want)
		}
	}
}
