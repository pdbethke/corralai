// SPDX-License-Identifier: Elastic-2.0

package cisogate

import "testing"

func TestDescribeResult(t *testing.T) {
	if got := describeResult(CisoResult{}); got != "no CISO controls apply" {
		t.Errorf("empty: %q", got)
	}
	allPass := CisoResult{Pass: true, Results: []CisoTestResult{{Goal: "g1", Target: "t1", Passed: true}, {Goal: "g2", Target: "t2", Passed: true}}}
	if got := describeResult(allPass); got != "all 2 CISO controls passed" {
		t.Errorf("all-pass: %q", got)
	}
	oneFail := CisoResult{Pass: false, Results: []CisoTestResult{{Goal: "g1", Target: "t1", Passed: true}, {Goal: "g2", Target: "t2", Passed: false}}}
	if got := describeResult(oneFail); got != "1/2 CISO controls FAILED: g2@t2" {
		t.Errorf("one-fail: %q", got)
	}
}
