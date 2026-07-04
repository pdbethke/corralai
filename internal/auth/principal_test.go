// SPDX-License-Identifier: Elastic-2.0

package auth

import "testing"

func TestPickPrincipal(t *testing.T) {
	cases := []struct {
		name                                    string
		email, preferredUsername, clientID, azp string
		want                                    string
	}{
		{"email wins", "a@x.com", "auser", "acli", "aazp", "a@x.com"},
		{"preferred_username when no email", "", "auser", "acli", "aazp", "auser"},
		{"client_id for a service token (no email/username)", "", "", "corral-svc", "", "corral-svc"},
		{"azp when only azp present", "", "", "", "corral-svc", "corral-svc"},
		{"empty when nothing", "", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickPrincipal(c.email, c.preferredUsername, c.clientID, c.azp); got != c.want {
				t.Errorf("pickPrincipal(%q,%q,%q,%q) = %q, want %q",
					c.email, c.preferredUsername, c.clientID, c.azp, got, c.want)
			}
		})
	}
}
