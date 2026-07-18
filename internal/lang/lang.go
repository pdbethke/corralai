// SPDX-License-Identifier: Elastic-2.0

// Package lang is the language-plugin seam for corral's adversarial audit
// gate. A Plugin owns everything language-specific about grading one
// self-contained source file + its test suite: the jail workspace scaffold,
// the test command, the compile/type-check, the test-file naming convention,
// extension-based detection, a toolchain preflight, and the per-language LLM
// system prompts. Everything else in the gate is language-neutral.
package lang

// Plugin is everything the audit gate needs to grade one self-contained
// source file + its test suite in a given language.
type Plugin interface {
	Name() string                                    // "go", "python"
	Detect(codePath string) bool                     // by file extension
	Scaffold() map[string]string                     // base workspace files (go.mod / none)
	TestCmd() []string                               // default recursive test command
	CompileCheck(codePath, testPath string) []string // syntax/type check for the authored test
	TestPath(codePath string) string                 // sibling test path per convention
	Preflight() error                                // toolchain present? nil ok, else fail CLOSED
	PromptLang() string                              // human label, for verdict metadata + logs
	TestWriterSystem() string                        // language-specific test-writer system prompt
	MutantSystem() string                            // language-specific mutant-generator system prompt
}

var registry = map[string]Plugin{}

// Register adds a plugin to the registry. Called from plugin files' init().
func Register(p Plugin) { registry[p.Name()] = p }

// ByName resolves a plugin by its language name. Fail-closed: (nil,false)
// for anything not registered.
func ByName(name string) (Plugin, bool) {
	p, ok := registry[name]
	return p, ok
}

// Detect resolves a plugin by the code file's extension. Fail-closed:
// (nil,false) if no registered plugin claims the path.
func Detect(codePath string) (Plugin, bool) {
	for _, p := range registry {
		if p.Detect(codePath) {
			return p, true
		}
	}
	return nil, false
}
