// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// recordJUnit is the format word of a JUnit XML record (RecordBlock's
// format, FileRecordHook's format argument).
const recordJUnit = "junit"

// junitCase is one <testcase> of a JUnit XML record that has a <failure> or
// an <error> child. Every field is the record's own text, unchanged:
//
//   - Suite is the name of the innermost <testsuite> holding it (PHPUnit
//     nests a method's data sets in a <testsuite name="Class::method">);
//   - Class is its `class` attribute and ClassName its `classname`. They are
//     kept apart because they differ: PHPUnit 11.5.57 writes a namespaced
//     class as class="App\Tests\NTest" and classname="App.Tests.NTest", and
//     only the first is the name it prints and filters by. Other runners
//     write only classname. A reader takes the one its runner means and
//     never substitutes the other;
//   - Name is its `name` attribute (PHPUnit: the method, with `with data set
//     "x"` or `with data set #k` for a data set, and no arguments);
//   - File is its `file` attribute, "" when the runner writes none;
//   - Kind is "failure" or "error", the first of the two children it has.
type junitCase struct {
	Suite, Class, ClassName, Name, File string
	Kind                                string
}

// The two kinds of a failing testcase. JUnit's <failure> is an assertion
// that failed and <error> a test that raised; a reader that wants every
// failing test takes both.
const (
	junitFailure = "failure"
	junitError   = "error"
)

// junitFailures is every testcase of a JUnit XML record that failed or
// errored, in document order, nested <testsuite>s included.
//
// It is all or nothing: a record that is not well-formed XML, holds no
// <testsuites> or <testsuite> element at its top, or holds a second
// top-level element after it, is an error, and no
// partial list is returned. The caller then reads the runner's printed lines
// instead, as it would with no record at all. A record cut short is the case
// this guards: its failures so far are not every failure, and a list that
// names only some of them would be read as the whole account.
func junitFailures(body string) ([]junitCase, error) {
	dec := xml.NewDecoder(strings.NewReader(body))
	var (
		out    []junitCase
		suites []string
		cur    *junitCase
		depth  int
		root   bool
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("junit record: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			name := t.Name.Local
			if depth == 1 {
				if root {
					return nil, fmt.Errorf("junit record: a second top-level element <%s>", name)
				}
				if name != "testsuites" && name != "testsuite" {
					return nil, fmt.Errorf("junit record: top element is <%s>, not <testsuites> or <testsuite>", name)
				}
				root = true
			}
			switch name {
			case "testsuite":
				suites = append(suites, xmlAttr(t, "name"))
			case "testcase":
				c := junitCase{Class: xmlAttr(t, "class"), ClassName: xmlAttr(t, "classname"), Name: xmlAttr(t, "name"), File: xmlAttr(t, "file")}
				if len(suites) > 0 {
					c.Suite = suites[len(suites)-1]
				}
				cur = &c
			case junitFailure, junitError:
				if cur != nil && cur.Kind == "" {
					cur.Kind = name
				}
			}
		case xml.EndElement:
			depth--
			switch t.Name.Local {
			case "testsuite":
				if len(suites) > 0 {
					suites = suites[:len(suites)-1]
				}
			case "testcase":
				if cur != nil && cur.Kind != "" {
					out = append(out, *cur)
				}
				cur = nil
			}
		}
	}
	if !root {
		return nil, errors.New("junit record: no <testsuites> or <testsuite> element")
	}
	return out, nil
}

// junitRecord is the failing testcases of the run's record (RecordBlock)
// when it is a complete, well-formed JUnit block: ok is false when the
// output carries no complete block, the block is not JUnit, or junitFailures
// refuses its XML, and a row's reader then reads the runner's printed lines
// instead. The one door every JUnit-record row reads its record through.
func junitRecord(out string) ([]junitCase, bool) {
	format, body, ok := RecordBlock(out)
	if !ok || format != recordJUnit {
		return nil, false
	}
	cases, err := junitFailures(body)
	if err != nil {
		return nil, false
	}
	return cases, true
}

// xmlAttr is the value of the attribute named name on t, "" when it has none.
func xmlAttr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
