// SPDX-License-Identifier: Elastic-2.0

package testgen

import "strings"

// extractCode pulls Go source out of a model response. It handles a
// ```go-fenced block (fence + optional language tag on the fence's own
// line), a bare ```-fenced block, and a no-fence response (returned
// trimmed as-is). Only the first fenced block is considered.
func extractCode(resp string) string {
	const fence = "```"
	start := strings.Index(resp, fence)
	if start == -1 {
		return strings.TrimSpace(resp)
	}
	rest := resp[start+len(fence):]
	// Skip an optional language tag up to the end of the fence's own line.
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		rest = rest[nl+1:]
	} else {
		rest = ""
	}
	end := strings.Index(rest, fence)
	if end == -1 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}
