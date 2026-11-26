// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// phpSyntax is PHP's string and comment syntax for maskedSpans. `#` is not a
// line comment here, because `#[...]` is an attribute and code may follow it
// on the line; a `# comment` is then read as code, which can only make a
// part refused (an unnamed member), never merged wrongly.
var phpSyntax = literalSyntax{quotes: `"'`, lineComments: []string{"//"}, blockComment: [2]string{"/*", "*/"}}

var (
	phpClassLineRe = regexp.MustCompile(`(?m)^(?:(?:final|abstract|readonly)\s+)*class\s+([A-Za-z_]\w*)`)
	phpMethodRe    = regexp.MustCompile(`\bfunction\s+&?\s*([A-Za-z_]\w*)\s*\(`)
	phpConstRe     = regexp.MustCompile(`\bconst\s+(?:[A-Za-z_\\?][\w\\|?]*\s+)?([A-Za-z_]\w*)\s*=`)
	phpPropRe      = regexp.MustCompile(`\$([A-Za-z_]\w*)`)
	phpTraitUseRe  = regexp.MustCompile(`^\s*use\s+([^;{]+)`)
	phpTestAttrRe  = regexp.MustCompile(`#\[\s*\\?(?:PHPUnit\\Framework\\Attributes\\)?Test\s*[\](]|@test\b`)
)

// phpClassFile is one proven PHPUnit file read as the one class it must be.
type phpClassFile struct {
	declares, uses []string // header statements, each a whole line
	namespace      string   // "" when the file declares none
	head           string   // attributes and docblock before the class, then the class line through its `{`
	members        []phpMember
}

// phpMember is one member of the class: a method, a property, a constant or
// a trait use, with its leading docblock and attributes.
type phpMember struct {
	key    string // "method foo", "property bar", "const BAZ", "use Trait"
	name   string // the method's name, for a rename
	text   string
	isTest bool
}

// ConcatTests folds several proven PHPUnit files into ONE class, the class
// named after the file. PHPUnit 10 and 11 load one class per file, the one
// named after it, and report "Class X cannot be found" for a file declaring
// anything else, so a merged file of suffixed classes (InvoiceTest_s0m1,
// InvoiceTest_s0m2) ran nothing on 11.5; every proven test of a file
// declares the same class, the one the file is named for, and the merge is
// that class holding every part's members.
//
// The `<?php` tag once; `declare` and `use` lines once each, in the order
// first seen; one `namespace`, a part that declares another refused. The
// class line (with any attributes or docblock before it) must be the same in
// every part, or the part is refused: it says what the file is. Then every
// part's members: one written the same way in two parts is kept once (a
// shared setUp, property or helper), a TEST method two parts declare
// differently is suffixed with its mutant id, as a colliding test is in
// every other language (see TestConcatenator), and any other member two
// parts write differently is refused, because renaming a helper means
// rewriting its callers, the model's code. A part with anything outside the
// class but those header lines, or with a heredoc, whose body this merge
// does not read, is refused. A refused part is carried out as its own file
// with the reason (ConcatAuthored), and the merged file is run once on the
// unmodified code (authored_together), so a merge that does not hold is
// reported.
func (phpPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	if len(parts) == 1 {
		return parts[0].Source, nil
	}
	files := make([]phpClassFile, len(parts))
	for i, part := range parts {
		f, err := readPHPClassFile(part.Source)
		if err != nil {
			return "", fmt.Errorf("php: proven test %s cannot be merged: %w", part.MutantID, err)
		}
		files[i] = f
	}
	first := files[0]
	var declares, uses []string
	seen := map[string]bool{}
	addOnce := func(dst *[]string, line string) {
		if !seen[line] {
			seen[line] = true
			*dst = append(*dst, line)
		}
	}
	var members []phpMember
	byKey := map[string]phpMember{}
	names := map[string]bool{}
	for i, f := range files {
		id := parts[i].MutantID
		if f.namespace != first.namespace {
			return "", fmt.Errorf("php: proven test %s declares namespace %q where the merged file declares %q", id, f.namespace, first.namespace)
		}
		if f.head != first.head {
			return "", fmt.Errorf("php: proven test %s declares its class differently (%q), and the file must declare the one class it is named after", id, lastLine(f.head))
		}
		for _, d := range f.declares {
			addOnce(&declares, d)
		}
		for _, u := range f.uses {
			addOnce(&uses, u)
		}
		for _, m := range f.members {
			prev, dup := byKey[m.key]
			switch {
			case !dup:
			case prev.text == m.text:
				continue
			case m.isTest && prev.isTest:
				renamed := m.name + "_" + idSuffix(id)
				if names[renamed] {
					return "", fmt.Errorf("php: proven test %s: %s and its renamed %s are both taken", id, m.name, renamed)
				}
				m.text = renameOnce(m.text, phpMethodRe, m.name, renamed)
				m.key, m.name = "method "+renamed, renamed
			default:
				return "", fmt.Errorf("php: proven test %s writes %s differently from an earlier proven test, and it is not a test, so it cannot be renamed safely", id, m.key)
			}
			byKey[m.key] = m
			if m.name != "" {
				names[m.name] = true
			}
			members = append(members, m)
		}
	}
	var out strings.Builder
	out.WriteString("<?php\n")
	writeBlock := func(lines []string) {
		if len(lines) > 0 {
			out.WriteString("\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	writeBlock(declares)
	if first.namespace != "" {
		out.WriteString("\n" + first.namespace + "\n")
	}
	writeBlock(uses)
	out.WriteString("\n" + first.head + "\n")
	texts := make([]string, len(members))
	for i, m := range members {
		texts[i] = m.text
	}
	out.WriteString(strings.Join(texts, "\n\n"))
	out.WriteString("\n}\n")
	return out.String(), nil
}

// readPHPClassFile reads src as the one-class file a PHPUnit test must be.
func readPHPClassFile(src string) (phpClassFile, error) {
	var f phpClassFile
	if strings.Contains(src, "<<<") {
		return f, errors.New("it holds a heredoc or nowdoc, whose body this merge does not read")
	}
	body, ok := strings.CutPrefix(src, "<?php")
	if !ok {
		return f, errors.New("it does not open with <?php")
	}
	code := maskLiterals(body, phpSyntax)
	loc := phpClassLineRe.FindStringIndex(code)
	if loc == nil {
		return f, errors.New("it declares no class")
	}
	if more := phpClassLineRe.FindAllStringIndex(code[loc[1]:], -1); len(more) > 0 {
		return f, errors.New("it declares more than one class")
	}
	// The header: whole lines before the class line. A docblock, comment or
	// attribute line belongs to the class's head; a declare, namespace or use
	// line is hoisted; anything else is code outside the class.
	var head []string
	lines := strings.Split(body[:loc[0]], "\n")
	codeLines := strings.Split(code[:loc[0]], "\n")
	for j, line := range lines {
		c := strings.TrimSpace(codeLines[j])
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			if len(head) > 0 {
				head = append(head, "")
			}
		case c == "" || strings.HasPrefix(c, "#["):
			head = append(head, strings.TrimRight(line, " \t"))
		case strings.HasPrefix(c, "declare(") || strings.HasPrefix(c, "declare ("):
			f.declares = append(f.declares, t)
		case strings.HasPrefix(c, "namespace "):
			if f.namespace != "" {
				return f, errors.New("it declares two namespaces")
			}
			f.namespace = t
		case strings.HasPrefix(c, "use ") || strings.HasPrefix(c, "require") || strings.HasPrefix(c, "include"):
			f.uses = append(f.uses, t)
		default:
			return f, fmt.Errorf("it has code outside the class (%q)", t)
		}
	}
	open := strings.IndexByte(code[loc[0]:], '{')
	if open < 0 {
		return f, errors.New("its class has no body")
	}
	open += loc[0]
	close := matchingBrace(code, open)
	if close < 0 {
		return f, errors.New("its class body does not close")
	}
	if strings.TrimSpace(code[close+1:]) != "" {
		return f, errors.New("it has code after the class")
	}
	f.head = strings.TrimRight(strings.Join(head, "\n"), "\n")
	if f.head != "" {
		f.head += "\n"
	}
	f.head += strings.TrimRight(body[loc[0]:open+1], " \t")
	members, err := phpMembers(body[open+1:close], code[open+1:close])
	if err != nil {
		return f, err
	}
	f.members = members
	return f, nil
}

// phpMembers splits a class body into its members: each ends at a `;` or at
// the `}` that closes a block, both at the body's own depth.
func phpMembers(body, code string) ([]phpMember, error) {
	var out []phpMember
	depth, start := 0, 0
	for i := 0; i < len(code); i++ {
		end := -1
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		case ';':
			if depth == 0 {
				end = i + 1
			}
		}
		if end < 0 {
			continue
		}
		m, err := phpMemberOf(body[start:end], code[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
		start = end
	}
	if strings.TrimSpace(code[start:]) != "" {
		return nil, fmt.Errorf("its class body ends in an unfinished member (%q)", strings.TrimSpace(body[start:]))
	}
	return out, nil
}

func phpMemberOf(text, code string) (phpMember, error) {
	m := phpMember{text: strings.TrimRight(strings.TrimLeft(text, "\n"), " \t\n")}
	head := code
	if at := strings.IndexAny(code, "{;"); at >= 0 {
		head = code[:at+1]
	}
	switch {
	case phpTraitUseRe.MatchString(head):
		m.key = "use " + strings.TrimSpace(phpTraitUseRe.FindStringSubmatch(head)[1])
	case phpMethodRe.MatchString(head):
		m.name = phpMethodRe.FindStringSubmatch(head)[1]
		m.key = "method " + m.name
		m.isTest = strings.HasPrefix(strings.ToLower(m.name), "test") || phpTestAttrRe.MatchString(text[:len(head)])
	case phpConstRe.MatchString(head):
		m.key = "const " + phpConstRe.FindStringSubmatch(head)[1]
	case phpPropRe.MatchString(head):
		m.key = "property " + phpPropRe.FindStringSubmatch(head)[1]
	default:
		return m, fmt.Errorf("its class holds a member this merge cannot name (%q)", strings.TrimSpace(text))
	}
	return m, nil
}

// maskLiterals is s with every string and comment byte under syn replaced by
// a space, newlines kept, so offsets and lines match s.
func maskLiterals(s string, syn literalSyntax) string {
	b := []byte(s)
	for _, sp := range maskedSpans(s, syn) {
		for i := sp[0]; i < sp[1]; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

// matchingBrace is the index of the `}` closing the `{` at open in code
// (literals already masked), or -1.
func matchingBrace(code string, open int) int {
	depth := 0
	for i := open; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// renameOnce renames the first declaration re matches with name to renamed.
func renameOnce(text string, re *regexp.Regexp, name, renamed string) string {
	code := maskLiterals(text, phpSyntax)
	for _, loc := range re.FindAllStringSubmatchIndex(code, -1) {
		if code[loc[2]:loc[3]] == name {
			return text[:loc[2]] + renamed + text[loc[3]:]
		}
	}
	return text
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[strings.LastIndexByte(s, '\n')+1:]
}
