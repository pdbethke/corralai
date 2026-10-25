// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"slices"
	"strings"
)

var rspecFilters = []string{"-e", "--example", "-E", "--example-matches"}

// rspecListCmd collects without running (`--dry-run`) through the JSON
// formatter.
func rspecListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, rspecFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--dry-run", "--format", "json", testPath), true
}

// rspecParseList reads the JSON formatter's examples. The report is read from
// its first "{", since bundler and spec helpers can print before it.
func rspecParseList(stdout string) []string {
	var rep struct {
		Examples []struct {
			Description     string `json:"description"`
			FullDescription string `json:"full_description"`
		} `json:"examples"`
	}
	if !decodeReport(stdout, &rep) {
		return nil
	}
	var ids []string
	for _, e := range rep.Examples {
		ids = append(ids, idFromFull(e.FullDescription, e.Description))
	}
	return ids
}

// rspecSingleCmd runs the one example whose full description is id's full
// name: -E (--example-matches, rspec 3.12 and later) with \A and \z, which
// unlike -e's substring match cannot also select a longer description.
func rspecSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	if id == "" || !narrowable(argv, rspecFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "-E", `\A`+regexQuoter.Replace(lastFull(id))+`\z`, testPath), true
}

// rspecListUnits is the dry run rspecListCmd makes, over the operator's own
// files: its examples' files are the units.
func rspecListUnits(argv []string) ([]string, bool) {
	if _, _, ok := rspecUnitArgs(argv); !ok {
		return nil, false
	}
	return append(slices.Clone(argv), "--dry-run", "--format", "json"), true
}

// rspecParseUnits reads the JSON formatter's examples' file_path (each spec
// file once per example; ParseUnitList deduplicates). A report without an
// examples array is no listing.
func rspecParseUnits(stdout string) []string {
	var rep struct {
		Examples *[]struct {
			FilePath string `json:"file_path"`
		} `json:"examples"`
	}
	if !decodeReport(stdout, &rep) || rep.Examples == nil {
		return nil
	}
	files := []string{}
	for _, e := range *rep.Examples {
		if e.FilePath == "" {
			return nil
		}
		files = append(files, e.FilePath)
	}
	return files
}

// rspecUnitCmd runs one spec file: argv with the operator's files and
// directories replaced by it. rspec runs every path it is given (measured on
// rspec-core 3.13.6: `rspec units/rspec units/rspec/spec/a_spec.rb` ran all
// three examples of the directory), so they cannot stay; a path in .rspec is
// not added to one given on the command line (measured: `rspec spec/a_spec.rb`
// beside a .rspec naming spec/b_spec.rb ran a's two). --pattern does not
// filter a file named outright (measured), and -t applies inside the file as
// it did to the listing, which listed no file whose examples it filtered out.
func rspecUnitCmd(argv []string, unit string) ([]string, bool) {
	head, kept, ok := rspecUnitArgs(argv)
	if !ok || unit == "" || strings.HasPrefix(unit, "-") {
		return nil, false
	}
	return append(append(slices.Clone(head), kept...), unit), true
}

// rspecUnitArgs splits argv at its rspec word into the words up to it and
// the options after it, without the operator's file arguments. A file
// argument with a location (`a_spec.rb:12`, `a_spec.rb[1:2]`) selects part
// of the file: the file alone would run examples the operator excluded, so
// the command has no unit form; neither has one with a name filter.
func rspecUnitArgs(argv []string) (head, kept []string, ok bool) {
	i := wordIndex(argv, "rspec")
	if i < 0 || !narrowable(argv, rspecFilters...) {
		return nil, nil, false
	}
	kept, files, ok := splitFileArgs(argv[i+1:], true, rspecOptions.takesValue)
	if !ok {
		return nil, nil, false
	}
	for _, f := range files {
		if rspecLocation.MatchString(f) {
			return nil, nil, false
		}
	}
	return argv[:i+1], kept, true
}

var rspecLocation = regexp.MustCompile(`:\d+(:\d+)*$|\[[\d:,]+\]$`)
