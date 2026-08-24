// SPDX-License-Identifier: Elastic-2.0

package advpool

import "testing"

// "off"/"none" must mean the same thing everywhere. An operator who learns
// --shadow-model off must not discover --shadow-writer-model off was
// interpreted as a MODEL NAMED "off" and sent to a provider.
func TestResolveShadowWriterModelOffSemantics(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"off", ""},
		{"OFF", ""},
		{"none", ""},
		{"  None  ", ""},
		{"gemma4", "gemma4"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
	} {
		if got := ResolveOptionalModel(c.in, ""); got != c.want {
			t.Errorf("ResolveOptionalModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Off unless named: an empty RunSpec must not carry a challenger writer.
func TestRunSpecShadowWriterDefaultsOff(t *testing.T) {
	var rs RunSpec
	if rs.ShadowWriterModel != "" {
		t.Errorf("ShadowWriterModel = %q on a zero RunSpec, want empty — the seat is off unless named", rs.ShadowWriterModel)
	}
}
