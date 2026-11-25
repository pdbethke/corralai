// SPDX-License-Identifier: Elastic-2.0

// The shapes go test's printed `--- FAIL:` lines take (goFirstFailure,
// goFailedTests): a test whose own output ends without a newline has that
// output and the next heading go prints on one line.
package fail

import (
	"fmt"
	"testing"
)

func TestNoNewline(t *testing.T) {
	fmt.Print("partial line no newline")
	t.Fail()
}

func TestParent(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		fmt.Print("child partial")
		t.Fail()
	})
}

func TestHeadingInOutput(t *testing.T) {
	fmt.Print("look: --- FAIL: TestNotReal (0.00s)")
	t.Fail()
}

func TestPlain(t *testing.T) { t.Error("plain failure") }

func TestPasses(t *testing.T) {}
