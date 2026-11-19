// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"slices"
	"strings"
)

// phpunitListCmd is PHPUnit's own `--list-tests`, which collects without
// running.
func phpunitListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, "--filter") {
		return nil, false
	}
	return append(slices.Clone(argv), "--list-tests", testPath), true
}

// phpunitParseList reads `--list-tests`' ` - Ns\Class::method` lines into
// `Class::method`, the form TestNamesInSource gives the critic. A data-set
// suffix (`"name"` or `#0`) is dropped and the method listed once, since one
// run of the method runs every data set. The namespace is dropped after
// de-duplicating, so two classes sharing a short name in two namespaces are
// listed twice and ResolveListedTest leaves the name unresolved.
func phpunitParseList(stdout string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		fq, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
		if !ok {
			continue
		}
		class, method, ok := strings.Cut(fq, "::")
		if !ok || class == "" {
			continue
		}
		if i := strings.IndexAny(method, `"#`); i >= 0 {
			method = method[:i]
		}
		if method == "" || seen[class+"::"+method] {
			continue
		}
		seen[class+"::"+method] = true
		ids = append(ids, class[strings.LastIndex(class, `\`)+1:]+"::"+method)
	}
	return ids
}

// phpunitSingleCmd runs one test method, every data set of it, in whatever
// namespace its class is: PHPUnit matches --filter against
// `Ns\Class::method with data set "name"`.
func phpunitSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	class, method, ok := strings.Cut(id, "::")
	if !ok || class == "" || method == "" || !narrowable(argv, "--filter") {
		return nil, false
	}
	filter := `/(?:^|\\)` + regexQuoter.Replace(class) + "::" + regexQuoter.Replace(method) + `(?: with data set .*)?$/`
	return append(slices.Clone(argv), "--filter", filter, testPath), true
}

// phpunitListUnits is PHPUnit's own `--list-tests` over argv, run under
// phpunitUnitsSetup: the tests the command collects, after its
// configuration's suites and the operator's arguments, and the file PHP
// loaded each test class from. The same listing serves every PHPUnit from
// 9.6 to 12.5 (measured on 9.6.39, 10.5.67, 11.5.57 and 12.5.44).
// `--list-test-files`, which this row used before, exists only from PHPUnit
// 11 (9.6 and 10.5: `Unknown option "--list-test-files"`), so a project on
// an older PHPUnit had no unit listing at all.
func phpunitListUnits(argv []string) ([]string, bool) {
	if _, _, ok := phpunitUnitArgs(argv); !ok {
		return nil, false
	}
	return append(slices.Clone(argv), "--list-tests"), true
}

// phpunitUnitClassMarker leads each line phpunitUnitsPrepend prints: a test
// class and the file it was loaded from, tab-separated.
const phpunitUnitClassMarker = "corral-unit-class "

// phpunitUnitsPrepend is loaded by auto_prepend_file into the listing run.
// PHPUnit's `--list-tests` names classes and not files, and only PHP knows
// which file a class came from: a test class need not be autoloadable, since
// PHPUnit finds test files by walking the configured directories, so no
// reading of composer.json's PSR-4 map could name the file of every class.
// At shutdown, after PHPUnit has printed its list, this prints the file of
// every concrete TestCase subclass the process loaded. phpunitParseUnits
// keeps only the classes PHPUnit listed, so a test class that was loaded
// and not collected adds no file.
//
// GUARD FIRST, as phpCoveragePrepend does: the file is loaded into every PHP
// process the listing starts (`composer test` is one before phpunit is), and
// a process that never loaded PHPUnit prints nothing.
const phpunitUnitsPrepend = `<?php
register_shutdown_function(static function (): void {
    if (!defined('STDOUT') || !class_exists('PHPUnit\Framework\TestCase', false)) {
        return;
    }
    foreach (get_declared_classes() as $class) {
        if (!is_subclass_of($class, 'PHPUnit\Framework\TestCase')) {
            continue;
        }
        $r = new ReflectionClass($class);
        if ($r->isAbstract() || $r->getFileName() === false) {
            continue;
        }
        fwrite(STDOUT, "` + phpunitUnitClassMarker + `" . $class . "\t" . $r->getFileName() . "\n");
    }
});
`

// phpunitUnitsSetup is the listing's Runner.ListUnitsSetup: it writes
// phpunitUnitsPrepend into the listing's temporary directory and adds that
// directory to PHP's ini scan path. PHP_INI_SCAN_DIR and not `php -d`, for
// CoverageCmd's reason: `vendor/bin/phpunit` and `composer test` take no
// `-d`. The operator's own PHP_INI_SCAN_DIR is kept in front, and with none
// set the value opens with a colon, which PHP reads as its default scan
// directory.
const phpunitUnitsSetup = `cat > "$d/corral_units.php" <<'CORRAL_PHP_EOF'` + "\n" + phpunitUnitsPrepend + "CORRAL_PHP_EOF\n" +
	`printf 'auto_prepend_file=%s\n' "$d/corral_units.php" > "$d/zz-corral-units.ini" || exit 1` + "\n" +
	`export PHP_INI_SCAN_DIR="${PHP_INI_SCAN_DIR-}:$d"`

// phpunitListHeading is the line above `--list-tests`' list: "Available
// test(s):" on PHPUnit 9 and 10, "Available tests:" or "Available test:" on
// 11 and 12. An empty list still prints it (measured on 9.6 and 11.5).
var phpunitListHeading = regexp.MustCompile(`^Available tests?(\(s\))?:$`)

// phpunitListIgnoredOption is what PHPUnit 9 and 10 print when the command
// carries a group option beside `--list-tests`: "The --group and
// --list-tests options cannot be combined, --group is ignored". The list
// under it is of every test, not of the tests the command runs.
var phpunitListIgnoredOption = regexp.MustCompile(`^The --\S+ and --list-tests options cannot be combined`)

// phpunitParseUnits reads phpunitListUnits' output into the files of the
// classes PHPUnit listed, in list order. It is no listing (nil) with no
// heading (an error, another mode), when PHPUnit says it ignored an option
// of the command (the list is then not the command's own: PHPUnit 9 and 10
// under --group or --exclude-group, where 11 and 12 apply them), and when a
// listed class has no file line, since a listing that cannot name every
// unit is not trusted to name any.
func phpunitParseUnits(stdout string) []string {
	fileOf := map[string]string{}
	var classes []string
	seen := map[string]bool{}
	listed := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if phpunitListIgnoredOption.MatchString(line) {
			return nil
		}
		if rest, ok := strings.CutPrefix(line, phpunitUnitClassMarker); ok {
			if class, file, ok := strings.Cut(rest, "\t"); ok && class != "" && file != "" {
				fileOf[class] = file
			}
			continue
		}
		if phpunitListHeading.MatchString(line) {
			listed = true
			continue
		}
		if !listed {
			continue
		}
		fq, ok := strings.CutPrefix(line, " - ")
		if !ok {
			continue
		}
		if class, _, ok := strings.Cut(fq, "::"); ok && class != "" && !seen[class] {
			seen[class] = true
			classes = append(classes, class)
		}
	}
	if !listed {
		return nil
	}
	files := []string{}
	for _, class := range classes {
		file, ok := fileOf[class]
		if !ok {
			return nil
		}
		files = append(files, file)
	}
	return files
}

// phpunitUnitsCmd runs the test files: argv with the operator's file and
// directory arguments replaced by them. PHPUnit runs every argument it is
// given (measured: `phpunit tests tests/ATest.php` ran ATest twice beside
// BTest, 5 tests), and a file argument takes the place of the configured
// suites, --testsuite included (measured: `--testsuite other tests/ATest.php`
// ran ATest), so the options can all stay.
func phpunitUnitsCmd(argv []string, units []string) ([]string, bool) {
	head, kept, ok := phpunitUnitArgs(argv)
	if !ok || !fileUnits(units) {
		return nil, false
	}
	return append(append(slices.Clone(head), kept...), units...), true
}

// phpunitUnitArgs splits argv at its phpunit word into the words up to it
// and the options after it, without the file arguments. PHPUnit reads
// options after a file (measured: `tests/ATest.php --group slow` ran one
// test).
func phpunitUnitArgs(argv []string) (head, kept []string, ok bool) {
	i := wordIndex(argv, "phpunit")
	if i < 0 || !narrowable(argv, "--filter") {
		return nil, nil, false
	}
	kept, ok = dropFileArgs(argv[i+1:], true, phpunitOptions.takesValue)
	return argv[:i+1], kept, ok
}
