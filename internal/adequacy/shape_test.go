// SPDX-License-Identifier: Elastic-2.0

package adequacy

import "testing"

// Real hunks from the 2026-09-04 psf/requests runs (gemini-3.6-flash), and
// a few shapes it did not happen to plant. The classifier reads the hunk
// only — never the model's opinion of what it did.
func TestShapeOfHunkOnRealMutants(t *testing.T) {
	cases := []struct{ name, search, replace, want string }{
		{"api.py: HTTP verb literal", `    return request("delete", url, **kwargs)`, `    return request("get", url, **kwargs)`, ShapeConstantChanged},
		{"adapters: verify → False", "            conn = self.get_connection_with_tls_context(\n                request, verify, proxies=proxies, cert=cert\n            )", "            conn = self.get_connection_with_tls_context(\n                request, False, proxies=proxies, cert=cert\n            )", ShapeArgumentChanged},
		{"adapters: verify → bool(verify)", `        self.cert_verify(conn, request.url, verify, cert)`, `        self.cert_verify(conn, request.url, bool(verify), cert)`, ShapeArgumentChanged},
		{"adapters: statement dropped", "        self.cert_verify(conn, request.url, verify, cert)\n        url = self.request_url(request, proxies)", "        url = self.request_url(request, proxies)", ShapeCallRemoved},
		{"raise removed", "        if not url:\n            raise MissingSchema(msg)\n        return url", "        if not url:\n            pass\n        return url", ShapeExceptionDropped},
		{"== to !=", `    if resp.status_code == 200:`, `    if resp.status_code != 200:`, ShapeConditionNegated},
		{"not added", `    if is_valid(x):`, `    if not is_valid(x):`, ShapeConditionNegated},
		{"and to or", `    if a and b:`, `    if a or b:`, ShapeConditionNegated},
		{"< to <=", `    if len(buf) < limit:`, `    if len(buf) <= limit:`, ShapeBoundaryShifted},
		{"off by one", `    return items[:n]`, `    return items[:n - 1]`, ShapeBoundaryShifted},
		{"return value", `    return self._cache[key]`, `    return None`, ShapeReturnChanged},
		{"branch emptied", "    if retries > 0:\n        backoff(retries)", "    if retries > 0:\n        pass", ShapeBranchRemoved},
		{"go: != to ==", `	if err != nil {`, `	if err == nil {`, ShapeConditionNegated},
		{"no anchor", "", "whole file", ShapeOther},
	}
	for _, c := range cases {
		if got := ShapeOfHunk(c.search, c.replace); got != c.want {
			t.Errorf("%s: shape = %q, want %q", c.name, got, c.want)
		}
	}
}
