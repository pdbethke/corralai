// SPDX-License-Identifier: Elastic-2.0

package lang

import "strings"

// A one-unit command replaces the operator's file or package arguments with
// the unit and keeps every option, so a row must tell an option's value from
// a file argument. Each table below is one runner's own `--help` at the
// version the capture script installs, and a test re-reads that help
// (TestUnitOptionTablesMatchTheirHelp, over the help text the capture script
// saved, or `go help` itself for Go). An option a table does not list, before
// a bare word, makes the command unreadable and the row refuses it: a guess
// would either drop a value or keep a file.

// optionTable is one runner's options: those that read their value from the
// next word when it is not written with "=" (values), and those that never
// do (flags, including options whose value can only follow "=").
type optionTable struct{ values, flags map[string]bool }

func newOptionTable(values, flags string) optionTable {
	return optionTable{values: optionSet(values), flags: optionSet(flags)}
}

// takesValue is dropFileArgs' answer for the table. A single-dash option
// longer than two characters whose first two are a value option carries its
// value attached (`-Ilib`, `-j4`, `-dmemory_limit=1G`), and reads no word.
func (t optionTable) takesValue(flag string) (value, known bool) {
	switch {
	case t.values[flag]:
		return true, true
	case t.flags[flag]:
		return false, true
	case len(flag) > 2 && flag[0] == '-' && flag[1] != '-' && t.values[flag[:2]]:
		return false, true
	}
	return false, false
}

// goOptionsFrom is the Go whose `go help testflag`, `go help build` and
// `go help test` goTestOptions was read from.
const goOptionsFrom = "go1.26"

// goTestOptions are `go test`'s own options, test binary flags included,
// without their dash: go reads `-x` and `--x` alike, and a test flag also
// as `-test.x`. A boolean takes its value only after "=" (`-race=false`).
var goTestOptions = newOptionTable(`
C asmflags bench benchtime blockprofile blockprofilerate buildmode compiler
count covermode coverpkg coverprofile cpu cpuprofile exec fuzz
fuzzminimizetime fuzztime gccgoflags gcflags installsuffix ldflags list
memprofile memprofilerate mod modfile mutexprofile mutexprofilefraction o
outputdir overlay p parallel pgo pkgdir run shuffle skip tags timeout
toolexec trace vet`, `
a args artifacts asan benchmem buildvcs c cover failfast fullpath json
linkshared modcacherw msan n race short trimpath v work x`)

// goTestOption is goTestOptions' answer for one word of a go test command.
func goTestOption(flag string) (value, known bool) {
	name := strings.TrimPrefix(strings.TrimPrefix(flag, "-"), "-")
	return goTestOptions.takesValue(strings.TrimPrefix(name, "test."))
}

// gotestsumOptions is gotestsum v1.13.0's `--help`. --rerun-fails is
// printed `int[=2]`: pflag reads its value only after "=".
var gotestsumOptions = newOptionTable(`
-f --format --format-icons --hide-summary --jsonfile
--jsonfile-timing-events --junitfile --junitfile-project-name
--junitfile-testcase-classname --junitfile-testsuite-name --max-fails
--packages --post-run-command --rerun-fails-max-failures
--rerun-fails-report`, `
--debug --format-hide-empty-pkg --junitfile-hide-empty-pkg
--junitfile-hide-skipped-tests --no-color --raw-command --rerun-fails
--rerun-fails-abort-on-data-race --rerun-fails-run-root-test --version
--watch --watch-chdir --watch-clear`)

// rspecOptions is rspec-core 3.13's `--help`. `-p, --[no-]profile [COUNT]`
// is an optional value Ruby's OptionParser still reads from the next word
// when it is not a flag (measured: `rspec -p spec/a_spec.rb` took the file
// as the count), so it is a value here, and its negation --no-profile reads
// none (measured: `rspec --no-profile spec/a_spec.rb` ran the file);
// `--fail-fast[=COUNT]` and `--bisect[=verbose]` take theirs only after "=".
var rspecOptions = newOptionTable(`
-I -r --require -O --options --order --seed --failure-exit-code
--error-exit-code --drb-port -f --format -o --out --deprecation-out
-p --profile -P --pattern --exclude-pattern -e --example
-E --example-matches -t --tag --default-path`, `
--bisect --fail-fast --no-fail-fast --no-profile -X --drb --no-drb -b --backtrace
--force-color --force-colour --no-color --no-colour --dry-run -w
--warnings --only-failures -n --next-failure --init -v --version -h
--help`)

// phpunitOptions is PHPUnit 11.5's `--help`, value-ness checked against its
// CLI Builder's LONG_OPTIONS ("=" a value, "==" one only after "="). Five
// options the Builder takes and `--help` does not print are left out, and so
// refused before a bare word. --colors, --coverage-text and
// --stop-on-deprecation take a value only after "=" (measured: `phpunit
// --colors never tests/ATest.php` read "never" as a test file), though
// `--help` prints `--colors <flag>`.
var phpunitOptions = newOptionTable(`
--atleast-version --bootstrap --cache-directory --columns -c
--configuration --coverage-filter --coverage-clover --coverage-cobertura
--coverage-crap4j --coverage-html --coverage-php --coverage-xml
--default-time-limit --exclude-group --filter --exclude-filter
--generate-baseline --use-baseline --group --covers --uses
--requires-php-extension --include-path --list-tests-xml --log-junit
--log-teamcity --order-by --random-order-seed --testdox-html
--testdox-text --test-suffix --testsuite --exclude-testsuite
--log-events-text --log-events-verbose-text --extension -d`, `
--cache-result --do-not-cache-result --check-version
--check-php-configuration --colors --warm-coverage-cache --coverage-text
--only-summary-for-coverage-text --show-uncovered-for-coverage-text
--path-coverage --disallow-test-output --display-all-issues
--display-incomplete --display-skipped --display-deprecations
--display-phpunit-deprecations --display-errors --display-notices
--display-warnings --enforce-time-limit --ignore-baseline
--generate-configuration --globals-backup -h --help
--list-groups --list-suites
--list-test-files --list-tests --migrate-configuration --no-configuration
--no-coverage --no-logging --no-extensions --no-output --no-progress
--no-results --process-isolation --do-not-report-useless-tests
--reverse-list
--static-backup --stderr --fail-on-all-issues --fail-on-deprecation
--fail-on-phpunit-deprecation --fail-on-phpunit-warning
--fail-on-empty-test-suite --fail-on-incomplete --fail-on-notice
--fail-on-risky --fail-on-skipped --fail-on-warning
--do-not-fail-on-deprecation --do-not-fail-on-phpunit-deprecation
--do-not-fail-on-phpunit-warning --do-not-fail-on-empty-test-suite
--do-not-fail-on-incomplete --do-not-fail-on-notice
--do-not-fail-on-risky --do-not-fail-on-skipped --do-not-fail-on-warning
--stop-on-defect --stop-on-deprecation --stop-on-error --stop-on-failure
--stop-on-incomplete --stop-on-notice --stop-on-risky --stop-on-skipped
--stop-on-warning --strict-coverage --disable-coverage-ignore
--strict-global-state --teamcity --testdox --testdox-summary --version
--debug`)

// cargoTestOptions is cargo 1.90's `cargo test --help`. An optional value
// printed `[<NAME>]` (-p, --bin, --test, --target, ...) is read from the
// next word when it is not a flag, as clap does; --timings takes its value
// only after "=".
var cargoTestOptions = newOptionTable(`
--message-format --color --config -Z -p --package --exclude --bin
--example --test --bench -F --features -j --jobs --profile --target
--target-dir --manifest-path --lockfile-path`, `
--no-run --no-fail-fast --future-incompat-report -q --quiet -v --verbose
-h --help --workspace --all --lib --bins --examples --tests --benches
--all-targets --doc --all-features --no-default-features -r --release
--unit-graph --timings --ignore-rust-version --locked --offline --frozen`)

// libtestOptions is the test harness's own `--help` (a test binary built
// by cargo 1.90), the arguments after `cargo test --`.
var libtestOptions = newOptionTable(`
--logfile --test-threads --skip --color --format -Z --shuffle-seed`, `
--include-ignored --ignored --force-run-in-process --exclude-should-panic
--test --bench --list --no-capture --exact -q --quiet
--show-output --report-time --ensure-time --shuffle -h --help`)

// nextestRunOptions is cargo-nextest 0.9.148's `cargo nextest run --help`;
// nextestRunOnly are those `cargo nextest list --help` does not print,
// which a listing therefore drops (`list` refuses them: measured, "unexpected
// argument '--retries' found"). Both print --message-format, with different
// values, so the run's is dropped too.
var nextestRunOptions = newOptionTable(`
--color -p --package --exclude --bin --example --test --bench -F
--features --build-jobs --cargo-profile --target --target-dir
--manifest-path --cargo-message-format --config -Z --run-ignored
--partition --platform-filter -E --filterset --show-progress
--archive-file --archive-format --extract-to --cargo-metadata
--workspace-remap --binaries-metadata --target-dir-remap
--build-dir-remap --config-file --user-config-file --tool-config-file
-P --profile -R --rerun -j --test-threads --retries --flaky-result
--max-fail --debugger --tracer --no-tests --stress-count
--stress-duration --failure-output --success-output --status-level
--final-status-level --max-progress-running --message-format
--message-format-version`, `
--no-pager -v --verbose -h --help --workspace --all --lib --bins
--examples --tests --benches --all-targets --all-features
--no-default-features -r --release --unit-graph --timings --frozen
--locked --offline --cargo-quiet --cargo-verbose --ignore-rust-version
--future-incompat-report --ignore-default-filter --extract-overwrite
--persist-extract-tempdir --override-version-check --no-run --fail-fast
--no-fail-fast --no-capture --no-output-indent --hide-progress-bar
--no-input-handler`)

var nextestRunOnly = optionSet(`
-R --rerun --no-run -j --test-threads --retries --flaky-result --fail-fast
--no-fail-fast --max-fail --debugger --tracer --no-tests --no-capture
--stress-count --stress-duration --failure-output --success-output
--status-level --final-status-level --no-output-indent --hide-progress-bar
--no-input-handler --max-progress-running --message-format
--message-format-version`)
