// SPDX-License-Identifier: Elastic-2.0

package cisogate

import (
	"fmt"
	"strings"
)

// describeResult renders a CisoResult as a human-readable status
// description for the CISO-gate check posted on the PR.
func describeResult(res CisoResult) string {
	if len(res.Results) == 0 {
		return "no CISO controls apply"
	}
	var failed []string
	for _, r := range res.Results {
		if !r.Passed {
			failed = append(failed, r.Goal+"@"+r.Target)
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("all %d CISO controls passed", len(res.Results))
	}
	return fmt.Sprintf("%d/%d CISO controls FAILED: %s", len(failed), len(res.Results), strings.Join(failed, ", "))
}
