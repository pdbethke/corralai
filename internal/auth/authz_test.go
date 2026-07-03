// SPDX-License-Identifier: Elastic-2.0

package auth

import "testing"

func TestAuthorizer(t *testing.T) {
	open := NewAuthorizer(nil)
	if !open.Allowed("anyone@example.com") {
		t.Error("empty allowlist must allow any authenticated principal")
	}
	if open.Count() != 0 {
		t.Errorf("empty allowlist Count = %d", open.Count())
	}

	a := NewAuthorizer([]string{"Alice@X.com", " bob@x.com ", ""})
	if a.Count() != 2 {
		t.Errorf("Count = %d, want 2 (blank dropped)", a.Count())
	}
	if !a.Allowed("alice@x.com") {
		t.Error("must match case-insensitively")
	}
	if !a.Allowed("  BOB@X.COM  ") {
		t.Error("must trim + match case-insensitively")
	}
	if a.Allowed("eve@x.com") {
		t.Error("unlisted principal must be denied")
	}
	if a.Allowed("") {
		t.Error("empty principal must be denied when an allowlist is set")
	}
}
