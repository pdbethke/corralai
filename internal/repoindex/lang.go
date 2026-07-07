// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"path/filepath"
	"strings"
)

// langForExt maps the file extension of path to a language name (case-insensitive).
// Returns "" for unknown or missing extensions.
func langForExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	return extLang[ext]
}

// extLang maps lowercase file extensions (including the leading dot) to
// canonical language names. Unknown extensions map to "" (zero value).
var extLang = map[string]string{
	".go":   "go",
	".py":   "python",
	".ts":   "typescript",
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".rs":   "rust",
	".java": "java",
	".c":    "c",
	".h":    "c",
	".cc":   "cpp",
	".cpp":  "cpp",
	".cxx":  "cpp",
	".hpp":  "cpp",
	".cs":   "csharp",
	".rb":   "ruby",
	".php":  "php",
	".sh":   "bash",
	".bash": "bash",
}



// defTypes maps tree-sitter node type → kind label for top-level definitions per language.
// Node types were verified by parsing sample code with each grammar (Task 2 + Task 3 probes).
func defTypes(lang string) map[string]string {
	switch lang {
	case "go":
		// Verified: function_declaration, method_declaration, type_declaration.
		// type_declaration names come from the type_spec child (see extractName).
		return map[string]string{
			"function_declaration": "function",
			"method_declaration":   "method",
			"type_declaration":     "type",
		}
	case "python":
		// Verified: function_definition, class_definition.
		// decorated_definition is unwrapped in chunkSymbols before hitting this map.
		return map[string]string{
			"function_definition": "function",
			"class_definition":    "class",
		}
	case "typescript", "javascript":
		// Verified: function_declaration (may be wrapped in export_statement),
		// class_declaration, method_definition (inside class_body).
		// interface_declaration included; identical node types between TS and JS grammars.
		return map[string]string{
			"function_declaration":  "function",
			"method_definition":     "method",
			"class_declaration":     "class",
			"interface_declaration": "interface",
		}
	case "rust":
		// Verified: function_item (name field), struct_item (name field),
		// impl_item (no name field; name extracted from first type_identifier child).
		// enum_item and type_alias omitted for brevity; add if needed.
		return map[string]string{
			"function_item": "function",
			"struct_item":   "struct",
			"impl_item":     "impl",
		}
	case "java":
		// Verified: class_declaration (container; methods inside class_body).
		// interface_declaration for completeness.
		return map[string]string{
			"class_declaration":     "class",
			"interface_declaration": "interface",
		}
	case "c":
		// Verified: function_definition (name via declarator chain),
		// struct_specifier (name: type_identifier child).
		return map[string]string{
			"function_definition": "function",
			"struct_specifier":    "struct",
		}
	case "cpp":
		// Verified: function_definition (same declarator chain as C),
		// class_specifier (container; methods inside field_declaration_list),
		// struct_specifier.
		return map[string]string{
			"function_definition": "function",
			"class_specifier":     "class",
			"struct_specifier":    "struct",
		}
	case "csharp":
		// Verified: class_declaration (container; methods inside declaration_list).
		// interface_declaration for completeness.
		return map[string]string{
			"class_declaration":     "class",
			"interface_declaration": "interface",
		}
	case "ruby":
		// Verified: method (top-level functions; name field),
		// class (container; methods inside body_statement),
		// module (container).
		return map[string]string{
			"method": "function",
			"class":  "class",
			"module": "module",
		}
	case "php":
		// Verified: function_definition (name field → "name" node),
		// class_declaration (container; methods inside declaration_list).
		return map[string]string{
			"function_definition": "function",
			"class_declaration":   "class",
		}
	case "bash":
		// Verified: function_definition (name field → "word" node).
		// Both `f() {}` and `function f() {}` forms produce function_definition.
		return map[string]string{
			"function_definition": "function",
		}
	}
	return nil
}

// isContainer reports whether a top-level node type is a class-like container
// whose named children should be one-level nested as method chunks.
func isContainer(lang, nodeType string) bool {
	switch lang {
	case "python":
		return nodeType == "class_definition"
	case "typescript", "javascript":
		return nodeType == "class_declaration"
	case "java":
		return nodeType == "class_declaration"
	case "cpp":
		return nodeType == "class_specifier"
	case "csharp":
		return nodeType == "class_declaration"
	case "ruby":
		return nodeType == "class" || nodeType == "module"
	case "php":
		return nodeType == "class_declaration"
	case "rust":
		// impl_item is a container: methods are function_item inside declaration_list.
		return nodeType == "impl_item"
	}
	return false
}

// methodDefTypes returns node-type → kind map for method-like defs inside containers.
func methodDefTypes(lang string) map[string]string {
	switch lang {
	case "python":
		// Inside a class_definition, function_definition nodes are methods.
		// decorated_definition wrapping a function_definition is also handled
		// in findMethodsInContainer (unwrapped there).
		return map[string]string{"function_definition": "method"}
	case "typescript", "javascript":
		// Inside a class_declaration, method_definition nodes are methods.
		return map[string]string{"method_definition": "method"}
	case "java":
		// Inside class_body: method_declaration nodes.
		return map[string]string{"method_declaration": "method"}
	case "cpp":
		// Inside field_declaration_list: function_definition nodes.
		return map[string]string{"function_definition": "method"}
	case "csharp":
		// Inside declaration_list: method_declaration nodes.
		return map[string]string{"method_declaration": "method"}
	case "ruby":
		// Inside body_statement: method nodes.
		return map[string]string{"method": "method"}
	case "php":
		// Inside declaration_list: method_declaration nodes.
		return map[string]string{"method_declaration": "method"}
	case "rust":
		// Inside declaration_list of impl_item: function_item nodes.
		return map[string]string{"function_item": "method"}
	}
	return nil
}
