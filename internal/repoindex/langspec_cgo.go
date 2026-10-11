//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	golang "github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/php"
	python "github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	typescript "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/pdbethke/corralai/internal/pairing"
)

// langSpec is everything repoindex knows about parsing one language. It used
// to be seven switches in four files, so adding a language meant seven edits
// and a silent miss if one was forgotten; it is one row per language now.
//
// A zero field means "this language has none", exactly as a missing switch
// arm did: a nil map answers every lookup with its zero value, a nil extract
// makes ExtractSignatures return ErrUnsupportedLang, and an empty boolOp
// counts no binary expression.
type langSpec struct {
	// grammar returns the tree-sitter grammar that parses the language.
	grammar func() *sitter.Language

	// defs maps tree-sitter node type → kind label for top-level
	// definitions. Node types were verified by parsing sample code with each
	// grammar (Task 2 + Task 3 probes).
	defs map[string]string

	// containers are the top-level node types that are class-like containers
	// whose named children should be one-level nested as method chunks.
	containers map[string]bool

	// methods maps node type → kind for method-like defs inside containers.
	methods map[string]string

	// extract returns the language's callable surface; nil → ErrUnsupportedLang.
	extract func(text, lang string) ([]Signature, error)

	// branches are the tree-sitter node types that introduce an independent
	// execution path through a symbol. Counting them (plus the boolean
	// operators handled by boolOp) yields a cyclomatic-style complexity.
	//
	// Deliberately per-language: a "case_clause" in Python and an
	// "expression_case" in Go are the same concept under different grammar
	// names, so a shared set would silently under-count one language and make
	// its symbols look easy.
	branches map[string]bool

	// boolOp names the node type whose `operator` field must be inspected for
	// && / || (and Ruby's and/or). These are ordinary binary expressions
	// rather than distinct node types, so they cannot live in branches:
	// counting every binary expression would charge arithmetic as a branch.
	// Python has none because its `boolean_operator` IS its own node type and
	// falls out of its branches set. "" if none.
	boolOp string

	// modules reads the test modules a source file declares in itself, for
	// languages whose tests may live in the file under audit (Rust's
	// `#[cfg(test)] mod tests`). nil → TestModules answers nil: the language
	// pairs by filename alone.
	modules func(root *sitter.Node, src []byte) []pairing.TestModule
}

// langSpecs holds one row per language repoindex can parse. It is filled in
// init rather than by a static initializer because the extractors it names
// parse through grammar, which reads langSpecs: a static initializer would be
// an initialization cycle (langSpecs → extractGoSignatures → parseTS →
// grammar → langSpecs), and init runs before any caller can parse.
var langSpecs map[string]langSpec

func init() {
	langSpecs = map[string]langSpec{
		"go": {
			grammar: golang.GetLanguage,
			// Verified: function_declaration, method_declaration, type_declaration.
			// type_declaration names come from the type_spec child (see extractName).
			defs: map[string]string{
				"function_declaration": "function",
				"method_declaration":   "method",
				"type_declaration":     "type",
			},
			extract: func(text, _ string) ([]Signature, error) { return extractGoSignatures(text) },
			branches: map[string]bool{
				"if_statement":       true,
				"for_statement":      true,
				"expression_case":    true,
				"type_case":          true,
				"communication_case": true,
				"select_statement":   true,
			},
			boolOp: "binary_expression",
		},
		"python": {
			grammar: python.GetLanguage,
			// Verified: function_definition, class_definition.
			// decorated_definition is unwrapped in chunkSymbols before hitting this map.
			defs: map[string]string{
				"function_definition": "function",
				"class_definition":    "class",
			},
			containers: map[string]bool{"class_definition": true},
			// Inside a class_definition, function_definition nodes are methods.
			// decorated_definition wrapping a function_definition is also handled
			// in findMethodsInContainer (unwrapped there).
			methods: map[string]string{"function_definition": "method"},
			extract: func(text, _ string) ([]Signature, error) { return extractPythonSignatures(text) },
			branches: map[string]bool{
				"if_statement":           true,
				"elif_clause":            true,
				"for_statement":          true,
				"while_statement":        true,
				"except_clause":          true,
				"case_clause":            true,
				"conditional_expression": true,
				"boolean_operator":       true, // `and` / `or`
			},
		},
		"typescript": {
			grammar:    typescript.GetLanguage,
			defs:       jsFamilyDefs,
			containers: jsFamilyContainers,
			methods:    jsFamilyMethods,
			extract:    extractCurlySignatures,
			branches:   jsFamilyBranches,
			boolOp:     jsFamilyBoolOp,
		},
		"javascript": {
			grammar:    javascript.GetLanguage,
			defs:       jsFamilyDefs,
			containers: jsFamilyContainers,
			methods:    jsFamilyMethods,
			extract:    extractCurlySignatures,
			branches:   jsFamilyBranches,
			boolOp:     jsFamilyBoolOp,
		},
		"rust": {
			grammar: rust.GetLanguage,
			// Verified: function_item (name field), struct_item (name field),
			// impl_item (no name field; name extracted from first type_identifier child).
			// enum_item and type_alias omitted for brevity; add if needed.
			defs: map[string]string{
				"function_item": "function",
				"struct_item":   "struct",
				"impl_item":     "impl",
			},
			// impl_item is a container: methods are function_item inside declaration_list.
			containers: map[string]bool{"impl_item": true},
			// Inside declaration_list of impl_item: function_item nodes.
			methods: map[string]string{"function_item": "method"},
			extract: func(text, _ string) ([]Signature, error) { return extractRustSignatures(text) },
			// match_arm, not match_expression: one decision per arm, as PHP's
			// case_statement does. `else` is the fall-through, not a decision,
			// and `loop` has no condition (its exits are the breaks and ifs it
			// contains); `?` is left out so the number stays comparable to the
			// other rows' explicit-branch counts.
			branches: map[string]bool{
				"if_expression":    true, // also `if let`, and `else if` (an if inside the else)
				"while_expression": true, // also `while let`
				"for_expression":   true,
				"match_arm":        true,
			},
			boolOp:  "binary_expression",
			modules: rustTestModules,
		},
		"java": {
			grammar: java.GetLanguage,
			// Verified: class_declaration (container; methods inside class_body).
			// interface_declaration for completeness.
			defs: map[string]string{
				"class_declaration":     "class",
				"interface_declaration": "interface",
			},
			containers: map[string]bool{"class_declaration": true},
			// Inside class_body: method_declaration nodes.
			methods: map[string]string{"method_declaration": "method"},
		},
		"c": {
			grammar: c.GetLanguage,
			// Verified: function_definition (name via declarator chain),
			// struct_specifier (name: type_identifier child).
			defs: map[string]string{
				"function_definition": "function",
				"struct_specifier":    "struct",
			},
		},
		"cpp": {
			grammar: cpp.GetLanguage,
			// Verified: function_definition (same declarator chain as C),
			// class_specifier (container; methods inside field_declaration_list),
			// struct_specifier.
			defs: map[string]string{
				"function_definition": "function",
				"class_specifier":     "class",
				"struct_specifier":    "struct",
			},
			containers: map[string]bool{"class_specifier": true},
			// Inside field_declaration_list: function_definition nodes.
			methods: map[string]string{"function_definition": "method"},
		},
		"csharp": {
			grammar: csharp.GetLanguage,
			// Verified: class_declaration (container; methods inside declaration_list).
			// interface_declaration for completeness.
			defs: map[string]string{
				"class_declaration":     "class",
				"interface_declaration": "interface",
			},
			containers: map[string]bool{"class_declaration": true},
			// Inside declaration_list: method_declaration nodes.
			methods: map[string]string{"method_declaration": "method"},
		},
		"ruby": {
			grammar: ruby.GetLanguage,
			// Verified: method (top-level functions; name field),
			// class (container; methods inside body_statement),
			// module (container).
			defs: map[string]string{
				"method": "function",
				"class":  "class",
				"module": "module",
			},
			containers: map[string]bool{"class": true, "module": true},
			// Inside body_statement: method nodes.
			methods: map[string]string{"method": "method"},
			extract: func(text, _ string) ([]Signature, error) { return extractRubySignatures(text) },
			branches: map[string]bool{
				"if":     true,
				"unless": true,
				"while":  true,
				"until":  true,
				"for":    true,
				// `when`, not `case`: counting the case statement AND each of its
				// branches would charge one extra path per switch, which is not what
				// gocyclo/radon/eslint do.
				"when":        true,
				"rescue":      true,
				"conditional": true, // a ? b : c
			},
			boolOp: "binary",
		},
		"php": {
			grammar: php.GetLanguage,
			// Verified: function_definition (name field → "name" node),
			// class_declaration (container; methods inside declaration_list).
			defs: map[string]string{
				"function_definition": "function",
				"class_declaration":   "class",
			},
			containers: map[string]bool{"class_declaration": true},
			// Inside declaration_list: method_declaration nodes.
			methods: map[string]string{"method_declaration": "method"},
			extract: func(text, _ string) ([]Signature, error) { return extractPHPSignatures(text) },
			branches: map[string]bool{
				"if_statement":                 true,
				"else_if_clause":               true,
				"foreach_statement":            true,
				"while_statement":              true,
				"do_statement":                 true,
				"catch_clause":                 true,
				"case_statement":               true,
				"match_conditional_expression": true,
				"conditional_expression":       true, // a ? b : c
				// default_statement / match_default_expression are deliberately
				// absent — the default arm is the fall-through, not an independent
				// decision, mirroring how the JS extractor treats switch_default.
			},
			boolOp: "binary_expression",
		},
		"bash": {
			grammar: bash.GetLanguage,
			// Verified: function_definition (name field → "word" node).
			// Both `f() {}` and `function f() {}` forms produce function_definition.
			defs: map[string]string{
				"function_definition": "function",
			},
		},
	}
}

// The javascript and typescript rows share everything but their grammar: the
// TS grammar is a superset of JS and names these nodes identically, so
// writing the values twice would only create an opportunity for the two to
// drift. (Same pattern as internal/lang's jsFamilyRules.)

// jsFamilyDefs — Verified: function_declaration (may be wrapped in
// export_statement), class_declaration, method_definition (inside
// class_body). interface_declaration included; identical node types between
// TS and JS grammars.
var jsFamilyDefs = map[string]string{
	"function_declaration":  "function",
	"method_definition":     "method",
	"class_declaration":     "class",
	"interface_declaration": "interface",
}

var jsFamilyContainers = map[string]bool{"class_declaration": true}

// jsFamilyMethods — inside a class_declaration, method_definition nodes are methods.
var jsFamilyMethods = map[string]string{"method_definition": "method"}

const jsFamilyBoolOp = "binary_expression"

// jsFamilyBranches — switch_default is deliberately absent: a default arm is
// the fall-through, not an independent decision, and neither eslint nor
// gocyclo counts it.
var jsFamilyBranches = map[string]bool{
	"if_statement":       true,
	"for_statement":      true,
	"for_in_statement":   true,
	"while_statement":    true,
	"do_statement":       true,
	"switch_case":        true,
	"catch_clause":       true,
	"ternary_expression": true,
}
