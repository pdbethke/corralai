// SPDX-License-Identifier: Elastic-2.0

package lang

// The per-plugin methods the runner table replaces, kept only until every
// caller asks the table instead. This file is deleted whole when they do.

func (goPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	return goSingleTestCmd(testPath, selector)
}
func (goPlugin) ListTestsCmd(testPath string) ([]string, bool) { return goListTestsCmd(testPath) }
func (goPlugin) ParseTestList(output string) []string          { return goParseTestList(output) }
func (goPlugin) FirstFailure(output []byte) string             { return goFirstFailure(output) }

func (pyPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	return pySingleTestCmd(testPath, selector)
}
func (pyPlugin) ListTestsCmd(testPath string) ([]string, bool) { return pyListTestsCmd(testPath) }
func (pyPlugin) ParseTestList(output string) []string          { return pyParseTestList(output) }
func (pyPlugin) FailedTests(output string) []string            { return pyFailedTests(output) }
func (pyPlugin) DeselectArgs(selectors []string) []string      { return pyDeselectArgs(selectors) }
func (pyPlugin) FirstFailure(output []byte) string             { return pyFirstFailure(output) }

func (phpPlugin) FirstFailure(output []byte) string { return phpFirstFailure(output) }

func (rustPlugin) FirstFailure(output []byte) string { return rustFirstFailure(output) }
func (rustPlugin) FlakyTests(output []byte) []string { return rustFlakyTests(output) }

func (rustPlugin) ListTestsCmdFor(testCmd []string) ([]string, bool) {
	if isNextest(testCmd) {
		return nextestListCmd(testCmd, "")
	}
	return libtestListCmd(testCmd, "")
}

func (rustPlugin) ParseTestListFor(testCmd []string, output string) []string {
	if isNextest(testCmd) {
		return nextestParseList(output)
	}
	return libtestParseList(output)
}

func (rustPlugin) SingleTestCmdFor(testCmd []string, id string) ([]string, bool) {
	if isNextest(testCmd) {
		return nextestSingleCmd(testCmd, "", id)
	}
	return libtestSingleCmd(testCmd, "", id)
}
