// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"strings"
)

// shWord is one shell word: raw is its text as written, text what the shell
// makes of its quoting (quotes removed, backslash escapes resolved).
// Expansions ($x, ${x}, $(…)) are left in text as written: what they expand
// to is decided at run time, never here.
type shWord struct{ raw, text string }

// shellCommands splits a POSIX sh script into its simple commands, each as
// the words it is made of, honouring the shell's quoting: inside '…', inside
// "…" and after a backslash, a space, a `;`, a `|` or a `#` is text, so a
// quoted word is ONE word whatever it holds. Redirections and their targets
// (`>&2`, `2>&1`, `< in`, `<<'EOF'`) are not words of the command. The
// commands a command substitution runs — `$(…)`, bare or inside double
// quotes — are returned too, before the command they appear in, because
// they run first.
//
// It reads corral's own wrappers, whose every operator word is single-quoted
// (shellQuote), and the operator's own `sh -c` scripts. It is a lexer for
// those, not a full shell grammar: compound commands (`if`, `case`, `{ … }`)
// come out as commands whose first word is the keyword, which
// shellCommandWords passes over (coverageTransparentWord), and backquoted
// substitutions are kept as text. An unterminated quote runs to the end of
// the script, as one word.
//
// The quoting is the point (ruling R29, final review I3). The lexer it
// replaces split at every `;` and space, quotes or not, so corral's own
// NODE_OPTIONS value broke into the junk program `}--require`, which hid
// the operator's quoted node path from the jail, and a repository's test
// file named `tests/x;/home/u/.local/bin/evil;.test.js` put
// /home/u/.local/bin/evil in command position and had the jail look it up.
func shellCommands(script string) [][]shWord {
	var out [][]shWord
	var cur []shWord
	var raw, text strings.Builder
	inWord, redirectTarget := false, false
	flushWord := func() {
		if !inWord {
			return
		}
		if redirectTarget {
			redirectTarget = false
		} else {
			cur = append(cur, shWord{raw: raw.String(), text: text.String()})
		}
		raw.Reset()
		text.Reset()
		inWord = false
	}
	flushCmd := func() {
		flushWord()
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur = nil
		redirectTarget = false
	}
	// substitution reads a `$(…)` starting at s[i] == '$', adding the
	// commands it runs to out, and returns the index after its `)`.
	substitution := func(s string, i int) int {
		end := shMatchParen(s, i+2)
		inner := s[i+2 : end]
		out = append(out, shellCommands(inner)...)
		next := min(end+1, len(s))
		raw.WriteString(s[i:next])
		text.WriteString(s[i:next])
		return next
	}
	s := script
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			flushWord()
			i++
		case c == '&' && i+1 < len(s) && s[i+1] == '>':
			// `&>file`, `&>>file`: both streams redirected, not a separator.
			flushWord()
			i += 2
			if i < len(s) && s[i] == '>' {
				i++
			}
			redirectTarget = true
		case c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			flushCmd()
			i++
		case c == '#' && !inWord:
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '<' || c == '>':
			// A word of digits right before the operator is its file
			// descriptor (`2>&1`), not an argument.
			if inWord && strings.Trim(raw.String(), "0123456789") == "" {
				raw.Reset()
				text.Reset()
				inWord = false
			} else {
				flushWord()
			}
			i++
			if i < len(s) && strings.IndexByte("<>&|", s[i]) >= 0 {
				i++
			}
			if i < len(s) && s[i] == '-' && i >= 2 && s[i-2:i] == "<<" {
				i++ // `<<-EOF`
			}
			redirectTarget = true
		case c == '\\':
			inWord = true
			switch {
			case i+1 >= len(s):
				i++
			case s[i+1] == '\n':
				i += 2 // a line continuation joins two lines
			default:
				raw.WriteString(s[i : i+2])
				text.WriteByte(s[i+1])
				i += 2
			}
		case c == '\'':
			inWord = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				end = len(s) - i - 1
			}
			raw.WriteString(s[i:min(i+1+end+1, len(s))])
			text.WriteString(s[i+1 : i+1+end])
			i = min(i+1+end+1, len(s))
		case c == '"':
			inWord = true
			raw.WriteByte('"')
			i++
			for i < len(s) && s[i] != '"' {
				switch {
				case s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0:
					raw.WriteString(s[i : i+2])
					if s[i+1] != '\n' {
						text.WriteByte(s[i+1])
					}
					i += 2
				case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
					i = substitution(s, i)
				default:
					raw.WriteByte(s[i])
					text.WriteByte(s[i])
					i++
				}
			}
			if i < len(s) {
				raw.WriteByte('"')
				i++
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			inWord = true
			i = substitution(s, i)
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			// `${x:-a b}` is one word, spaces and all.
			inWord = true
			end := strings.IndexByte(s[i:], '}')
			next := len(s)
			if end >= 0 {
				next = i + end + 1
			}
			raw.WriteString(s[i:next])
			text.WriteString(s[i:next])
			i = next
		default:
			inWord = true
			raw.WriteByte(c)
			text.WriteByte(c)
			i++
		}
	}
	flushCmd()
	return out
}

// shMatchParen is the index of the `)` closing a `$(` whose body starts at
// s[start], passing over quoted text and nested parentheses; len(s) when it
// never closes.
func shMatchParen(s string, start int) int {
	depth := 1
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '\'':
			if end := strings.IndexByte(s[i+1:], '\''); end >= 0 {
				i += end + 1
			} else {
				return len(s)
			}
		case '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s)
}

// shAssignment matches a word that is a variable assignment, `NAME=value`,
// read from the word as written: a quoted name is not an assignment.
var shAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)

// commandArgv is a simple command's words from the one in command position
// on: leading assignments and the transparent words (coverageTransparentWord:
// shell keywords and exec-style wrappers, after which the command is the
// next word) are passed over, and so is a bare number.
func commandArgv(cmd []shWord) []shWord {
	for i, w := range cmd {
		if shAssignment.MatchString(w.raw) || coverageTransparentWord[w.text] || strings.Trim(w.text, "0123456789") == "" {
			continue
		}
		return cmd[i:]
	}
	return nil
}
