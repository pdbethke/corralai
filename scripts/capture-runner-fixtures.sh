#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-runner-fixtures.sh [--keep] [DIR] — runs the real jest,
# vitest, mocha, node --test, rspec, phpunit, go test, gotestsum, cargo test
# and cargo nextest against the fixture suites in
# internal/lang/testdata/runners/src, writes what each runner's LIST command
# prints into internal/lang/testdata/runners/, and checks that each runner's
# one-test command really runs one test.
#
# Why: a list parser tested against output we typed is tested against what we
# expect the runner to print. These fixtures are what it printed (spec:
# docs/superpowers/specs/2026-10-15-runner-table-design.md §2.4, "a capability
# ships only after it ran against the real runner"). The commands below are
# the ones the runner rows build (internal/lang/runner_*.go); keep them equal.
#
# Needs the network (npm, composer, go install) and node, ruby with rspec,
# php, go, cargo and cargo-nextest. DIR
# defaults to a temp dir, removed on exit unless --keep; a kept DIR is what
# CORRALAI_RUNNER_ROOT points TestRealRunnersListAndRunOne at. RSPEC overrides
# the rspec command (a box whose rspec shim is broken can pass `ruby -S ...`).
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="$PWD"
OUT="$REPO/internal/lang/testdata/runners"

KEEP=0
[ "${1:-}" = "--keep" ] && { KEEP=1; shift; }
DIR="${1:-$(mktemp -d)}"
[ "$KEEP" = 1 ] || trap 'rm -rf "$DIR"' EXIT
RSPEC="${RSPEC:-rspec}"

JEST=30.5.2 VITEST=3.2.7 MOCHA=11.8.0 PHPUNIT=11.5.57

mkdir -p "$DIR"
cp -r "$OUT/src/." "$DIR/"
cd "$DIR"
# The versions are spelled out here, not interpolated, so a pin scanner can read them;
# the guard below keeps them equal to the variables the manifest reports.
[ "$JEST $VITEST $MOCHA" = "30.5.2 3.2.7 11.8.0" ] || { echo "JEST/VITEST/MOCHA drifted from the npm install line" >&2; exit 1; }
npm install --silent --no-audit --no-fund jest@30.5.2 vitest@3.2.7 mocha@11.8.0 >/dev/null
[ -f vendor/bin/phpunit ] || composer require -q --dev "phpunit/phpunit:$PHPUNIT"

# --- list commands: stdout only, as the enumerator keeps it ---------------
npx jest --json --testNamePattern '^\b$' calc.jest.test.js 2>/dev/null | node -e \
  'const j=JSON.parse(require("fs").readFileSync(0));process.stdout.write(JSON.stringify({testResults:j.testResults.map(f=>({assertionResults:f.assertionResults.map(a=>({ancestorTitles:a.ancestorTitles,title:a.title,fullName:a.fullName,status:a.status}))}))},null,1))' \
  > "$OUT/jest-list.json"
npx vitest list calc.vitest.test.mjs 2>/dev/null > "$OUT/vitest-list.txt"
npx mocha --dry-run --reporter json calc.mocha.js 2>/dev/null | node -e \
  'const j=JSON.parse(require("fs").readFileSync(0));process.stdout.write(JSON.stringify({tests:j.tests.map(t=>({title:t.title,fullTitle:t.fullTitle}))},null,1))' \
  > "$OUT/mocha-list.json"
node --test --test-reporter=tap calc.node.test.js 2>/dev/null | grep -v duration_ms > "$OUT/node-tap.txt" || true
node --test --test-reporter=tap broken.node.test.js 2>/dev/null | grep -vE "duration_ms|$DIR" > "$OUT/node-tap-loadfail.txt" || true
$RSPEC --dry-run --format json spec/calc_spec.rb 2>/dev/null | ruby -rjson -e \
  'j=JSON.parse(STDIN.read); puts JSON.pretty_generate({"examples"=>j["examples"].map{|e| e.slice("id","description","full_description")}})' \
  > "$OUT/rspec-list.json"
$RSPEC --dry-run --format json spec/join_spec.rb 2>/dev/null | ruby -rjson -e \
  'j=JSON.parse(STDIN.read); puts JSON.pretty_generate({"examples"=>j["examples"].map{|e| e.slice("id","description","full_description")}})' \
  > "$OUT/rspec-join-list.json"
vendor/bin/phpunit --list-tests tests/CalcTest.php > "$OUT/phpunit-list.txt"

# --- unit listings (internal/lang/runner_units.go): ListUnitsCmd's command --
# runs under a prelude printing the working directory, which jest and vitest
# need because they list absolute paths. The directory is written as /fixture:
# these files are public.
PRELUDE='printf '"'"'corral-units-cwd %s\n'"'"' "$(pwd -P)" && exec "$@"'
PDIR="$(pwd -P)"
units() { # units <fixture> <cmd...>: the wrapped listing's stdout
  local f="$1"; shift
  sh -c "$PRELUDE" corral-units "$@" 2>/dev/null | sed -e "s#$PDIR#/fixture#g" -e "s#$HOME#~#g" -e '/duration_ms/d' > "$OUT/$f" || true
}
units jest-units.txt npx --no-install jest --ci units/jest --listTests --json
units vitest-units.txt npx --no-install vitest list --reporter=verbose units/vitest --filesOnly --json
units vitest-units-collide.txt npx --no-install vitest list units/collide --filesOnly --json
units node-units.txt node --test --test-only --test-reporter=tap --test-concurrency 1 units/node/a.node.test.js units/node/b.node.test.js
units node-units-only.txt node --test --test-only --test-reporter=tap 'units/node-only/*.test.js' 'units/node/*.test.js'

# --- one-test commands: each must run exactly the tests named --------------
fail=0
want() { # want <runner> <expected> <got>
  if [ "$2" = "$3" ]; then echo "ok   $1: ran $3"; else echo "FAIL $1: ran $3, want $2"; fail=1; fi
}
want jest 1 "$(npx jest --json --testNamePattern '^Calc adds \(a\+b\)$' calc.jest.test.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.numPassedTests+j.numFailedTests)')"
want vitest 1 "$(npx vitest run --reporter=json -t '^Calc neg adds$' calc.vitest.test.mjs 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.numPassedTests+j.numFailedTests)')"
want mocha-fullname-collision 2 "$(npx mocha --reporter spec --reporter json --grep '^Calc adds$' calc.mocha.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.stats.tests)')"
want mocha 1 "$(npx mocha --reporter spec --reporter json --grep '^Calc neg adds$' calc.mocha.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.stats.tests)')"
want node 1 "$(node --test --test-reporter=tap --test-name-pattern '^Calc neg adds$' calc.node.test.js 2>/dev/null | sed -n 's/^# pass //p')"
want node-leaf-collision 2 "$(node --test --test-reporter=tap --test-name-pattern '^adds$' calc.node.test.js 2>/dev/null | sed -n 's/^# pass //p')"
want rspec 1 "$($RSPEC --format json -E '\ACalc adds \(a\+b\)\z' spec/calc_spec.rb 2>/dev/null | ruby -rjson -e 'puts JSON.parse(STDIN.read)["summary"]["example_count"]')"
want phpunit 1 "$(vendor/bin/phpunit --filter '/(?:^|\\)CalcTest::testAdds(?: with data set .*)?$/' tests/CalcTest.php | sed -n 's/^OK (\([0-9]*\) test.*/\1/p')"
want phpunit-dataset 2 "$(vendor/bin/phpunit --filter '/(?:^|\\)CalcTest::testWith(?: with data set .*)?$/' tests/CalcTest.php | sed -n 's/^OK (\([0-9]*\) test.*/\1/p')"

# One-unit commands (runner_units.go): units/*/a.* holds two tests and b.* one,
# so a run of the unit alone passes 2. The second line of each pair is why the
# row is shaped as it is: jest's --runTestsByPath also runs a file the operator
# named; vitest's file filter is a substring of the path below its root, here
# a.vitest.test.mjs of xa.vitest.test.mjs; mocha adds a configured spec to
# the files on its command line, so mocha has no unit form.
unitran() { grep -oE '(Tests:? +[0-9]+ passed)|(^# pass [0-9]+)|([0-9]+ passing)' | grep -oE '[0-9]+' | head -1; }
want jest-unit 2 "$(npx --no-install jest --ci units/jest --runTestsByPath units/jest/a.jest.test.js '--testPathIgnorePatterns=^(?!.*\/units\/jest\/a\.jest\.test\.js$)' 2>&1 | unitran)"
# Under projects each project has its own rootDir: a pattern anchored at
# <rootDir> matches nothing and, with --passWithNoTests, passes having run no
# test. The row's form names the unit by its path from the working directory.
want jest-rootDir-pattern-under-projects "no test, exit 0" "$(cd units/projects && npx --no-install jest --passWithNoTests '--testPathIgnorePatterns=^(?!<rootDir>\/p1\/a\.jest\.test\.js$)' >/dev/null 2>&1 && echo "no test, exit 0")"
(cd units/projects && npx --no-install jest --passWithNoTests --runTestsByPath p1/a.jest.test.js '--testPathIgnorePatterns=^(?!.*\/p1\/a\.jest\.test\.js$)' 2>&1) | sed -e 's/\x1b\[[0-9;]*m//g' -e "s#$DIR#\$DIR#g" -e '/^Time: \|^Ran all/d' > "$OUT/jest-unit-projects.txt"
want jest-unit-projects 2 "$(unitran < "$OUT/jest-unit-projects.txt")"
# A name filter set inside a bundle of short options filters too, which the
# rows refuse (jestNarrowable, vitestNarrowable): each ran 1 of a's 2 tests.
npx --no-install jest units/jest/a.jest.test.js -ct 'a one' 2>&1 | sed -e 's/\x1b\[[0-9;]*m//g' -e '/^Time: \|^Ran all/d' > "$OUT/jest-bundled-filter.txt"
jestpassed() { sed -n 's/^Tests:.* \([0-9]*\) passed.*/\1/p'; }
want jest-bundled-short-t-filters 1 "$(jestpassed < "$OUT/jest-bundled-filter.txt")"
want jest-it-filters 1 "$(npx --no-install jest units/jest/a.jest.test.js -it 'a one' 2>&1 | jestpassed)"
want vitest-ut-filters 1 "$(npx --no-install vitest run units/vitest/a.vitest.test.mjs -ut 'a one' </dev/null 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
# A shard of one file can be empty, and an empty run passes; UnitCmd drops it.
want node-shard-of-one-file-is-empty "0 exit 0" "$(out=$(node --test --test-reporter=tap --test-shard=2/2 units/node/a.node.test.js 2>&1) && echo "$(printf '%s' "$out" | unitran) exit 0")"
want jest-runTestsByPath-adds-named-file 3 "$(npx --no-install jest units/jest/b.jest.test.js --runTestsByPath units/jest/a.jest.test.js 2>&1 | unitran)"
want vitest-unit 2 "$(npx --no-install vitest run --reporter=verbose units/vitest/a.vitest.test.mjs 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
want vitest-unit-substring-collision 3 "$(npx --no-install vitest run --root units/collide units/collide/a.vitest.test.mjs 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
want node-unit 2 "$(node --test --test-reporter=tap --test-concurrency 1 units/node/a.node.test.js 2>&1 | unitran)"
want mocha-config-spec-merges 2 "$(cd units/mocharc && npx --no-install mocha t/a.mocha.js 2>&1 | unitran)"

# --- Go, Ruby, PHP and Rust units (runner_units.go's other rows) -----------
# Each listing is ListUnitsCmd's command for the operator command named in
# the comment above it; each want is a one-unit run, or a measurement behind
# the row's shape. a.* / package a / units-a's lib hold two tests, the rest one.
help() { # help <fixture> <cmd...>: a runner's --help, the tables in runner_unit_options.go
  local f="$1"; shift
  "$@" 2>&1 | sed -e "s#$DIR#\$DIR#g" -e 's#/tmp/go-build[0-9]*/b[0-9]*/exe/#$GOBUILD/#g' > "$OUT/$f" || true
}
GOTESTSUM=1.13.0
[ "$GOTESTSUM" = "1.13.0" ] || { echo "GOTESTSUM drifted from the install line" >&2; exit 1; }
GOBIN="$DIR/bin" go install gotest.tools/gotestsum@v1.13.0
# goUnitTemplate (go_units.go)
GOT='{{if .Error}}error {{.ImportPath}}{{else if or .TestGoFiles .XTestGoFiles}}unit {{.ImportPath}}{{else}}none {{.ImportPath}}{{end}}'
# go test -count=1 ./...  /  go test -tags extra ./...  /  go test ./nope
(cd units/gomod && units go-units.txt go list -e -f "$GOT" ./...)
(cd units/gomod && units go-units-tags.txt go list -tags extra -e -f "$GOT" ./...)
(cd units/gomod && units go-units-error.txt go list -e -f "$GOT" ./nope)
gopass() { grep -c -- '--- PASS' || true; }
want go-unit 2 "$(cd units/gomod && go test -v -count=1 example.com/units/a 2>&1 | gopass)"
want go-unit-xtest 1 "$(cd units/gomod && go test -v -count=1 example.com/units/b 2>&1 | gopass)"
want gotestsum-no-args-runs "go test -json ./..." "$(cd units/gomod && "$DIR/bin/gotestsum" --debug 2>&1 | sed -n 's/^exec: \[\(.*\)\]$/\1/p')"
want gotestsum-unit 2 "$(cd units/gomod && "$DIR/bin/gotestsum" --format standard-verbose -- -count=1 example.com/units/a 2>&1 | gopass)"
help gotestsum-help.txt "$DIR/bin/gotestsum" --help

# rspec units/rspec
units rspec-units.txt $RSPEC units/rspec --dry-run --format json
rbran() { grep -oE '[0-9]+ examples?' | grep -oE '[0-9]+' | tail -1; }
want rspec-unit 2 "$($RSPEC units/rspec/spec/a_spec.rb 2>&1 | rbran)"
want rspec-attached-e-filters 1 "$($RSPEC -eone units/rspec/spec/a_spec.rb 2>&1 | rbran)"
want rspec-paths-union 3 "$($RSPEC units/rspec units/rspec/spec/a_spec.rb 2>&1 | rbran)"
want rspec-dotrspec-path-not-added 2 "$(cd units/rspecrc && $RSPEC spec/a_spec.rb 2>&1 | rbran)"
want rspec-profile-reads-next-word 3 "$(cd units/rspec && $RSPEC -p spec/a_spec.rb 2>&1 | rbran)"
want rspec-no-profile-reads-no-word 2 "$(cd units/rspec && $RSPEC --no-profile spec/a_spec.rb 2>&1 | rbran)"
help rspec-help.txt $RSPEC --help

# vendor/bin/phpunit -c units/phpunit/phpunit.xml --testsuite unit  /  ... --group slow
PHPC="vendor/bin/phpunit -c units/phpunit/phpunit.xml"
units phpunit-units.txt $PHPC --testsuite unit --list-test-files
units phpunit-units-group.txt $PHPC --group slow --list-test-files
phpran() { sed -n 's/^OK (\([0-9]*\) tests\{0,1\}.*/\1/p'; }
want phpunit-unit 2 "$($PHPC --testsuite unit units/phpunit/tests/ATest.php 2>&1 | phpran)"
want phpunit-args-union 5 "$($PHPC units/phpunit/tests units/phpunit/tests/ATest.php 2>&1 | phpran)"
want phpunit-file-replaces-testsuite 2 "$($PHPC --testsuite other units/phpunit/tests/ATest.php 2>&1 | phpran)"
want phpunit-option-after-file 1 "$($PHPC units/phpunit/tests/ATest.php --group slow 2>&1 | phpran)"
want phpunit-exclude-group-lists-no-empty-file 0 "$($PHPC --exclude-group slow --list-test-files units/phpunit/excl units/phpunit/tests 2>&1 | grep -c DTest || true)"
want phpunit-colors-takes-no-next-word 'Test file "never" not found' "$($PHPC --colors never units/phpunit/tests/ATest.php 2>&1 | grep -o 'Test file "never" not found')"
help phpunit-help.txt vendor/bin/phpunit --help

# cargo test -p units-a  /  cargo test --workspace  /  cargo nextest run --workspace [integrates]
cargoran() { grep -oE 'test result: ok\. [0-9]+ passed' | grep -oE '[0-9]+' | awk '{s+=$1} END {print s+0}'; }
nextran() { sed -n 's/^ *Summary .* \([0-9]*\) tests\{0,1\} run:.*/\1/p'; }
(cd units/cargo && units cargo-units.txt cargo test -p units-a --no-run --message-format=json)
(cd units/cargo && units cargo-units-workspace.txt cargo test --workspace --no-run --message-format=json)
(cd units/cargo && units nextest-units.txt cargo nextest list --workspace --message-format json)
(cd units/cargo && units nextest-units-filter.txt cargo nextest list --workspace integrates --message-format json)
want cargo-unit-lib 2 "$(cd units/cargo && cargo test -p units-a --lib 2>&1 | cargoran)"
want cargo-unit-bin 1 "$(cd units/cargo && cargo test -p units-a --bin units-a 2>&1 | cargoran)"
want cargo-unit-test 1 "$(cd units/cargo && cargo test -p units-a --test it 2>&1 | cargoran)"
want cargo-targets-union 3 "$(cd units/cargo && cargo test -p units-a --test it --lib 2>&1 | cargoran)"
want cargo-target-name-across-packages 2 "$(cd units/cargo && cargo test --workspace --test it 2>&1 | cargoran)"
# -p adds to --workspace rather than narrowing it, so a unit run replaces the
# operator's package selection with -p <its package>; each runs only its own.
want cargo-workspace-plus-p-adds 3 "$(cd units/cargo && cargo test --workspace -p units-a --lib 2>&1 | cargoran)"
want cargo-unit-b-lib 1 "$(cd units/cargo && cargo test -p units-b --lib 2>&1 | cargoran)"
want cargo-unit-b-test 1 "$(cd units/cargo && cargo test -p units-b --test it 2>&1 | cargoran)"
want nextest-unit 2 "$(cd units/cargo && cargo nextest run --workspace -E 'binary_id(=units-a)' 2>&1 | nextran)"
want nextest-filtersets-union 3 "$(cd units/cargo && cargo nextest run --workspace -E 'binary_id(=units-a)' -E 'test(negates)' 2>&1 | nextran)"
want nextest-name-filter-and-binary 0 "$(cd units/cargo && cargo nextest run --workspace integrates -E 'binary_id(=units-a)' 2>&1 | nextran)"
# An -E written attached is a filterset too, ORed with the unit's.
want nextest-attached-filterset-ors 2 "$(cd units/cargo && cargo nextest run --workspace '-Etest(negates)' -E 'binary_id(=units-a::it)' 2>&1 | nextran)"
# Every run-only option (nextestRunOnly, runner_unit_options.go) is refused by list.
refused=0
for o in "-R x" "--rerun x" "--no-run" "-j 1" "--test-threads 1" "--retries 1" "--flaky-result pass" "--fail-fast" \
  "--no-fail-fast" "--max-fail 1" "--debugger gdb" "--tracer strace" "--no-tests pass" "--no-capture" "--stress-count 1" \
  "--stress-duration 1s" "--failure-output final" "--success-output never" "--status-level all" "--final-status-level all" \
  "--no-output-indent" "--hide-progress-bar" "--no-input-handler" "--max-progress-running 1" "--message-format libtest-json" \
  "--message-format-version 0.1"; do
  # shellcheck disable=SC2086 # each option and its value are two words
  out="$(cd units/cargo && cargo nextest list --workspace $o 2>&1 || true)"
  case "$out" in *"unexpected argument"* | *"invalid value"*) refused=$((refused + 1)) ;; esac
done
want nextest-list-refuses-every-run-only 26 "$refused"
help cargo-test-help.txt cargo test --help
help libtest-help.txt sh -c 'cd units/cargo && cargo test -q -p units-a --test it -- --help 2>/dev/null'
help nextest-run-help.txt cargo nextest run --help
help nextest-list-help.txt cargo nextest list --help

# --- failing suites: what each DEFAULT reporter prints, and the skip commands --
# (internal/lang/runner_failures.go). $DIR and $HOME are replaced: these files are public.
fails() { # fails <fixture> <cmd...>: stdout+stderr, ANSI stripped
  local f="$1"; shift
  "$@" 2>&1 | sed -e 's/\x1b\[[0-9;]*m//g' -e "s#$DIR#\$DIR#g" -e "s#$HOME#~#g" -e '/duration_ms\|^Finished in\|^Time: \|Duration \|Start at \|^Run options: --seed\|runs\/s/d' > "$OUT/$f" || true
}
fails jest-fail.txt npx jest fail/f.jest.test.js
fails jest-suitefail.txt npx jest fail/broken.jest.test.js
fails vitest-fail.txt npx vitest run fail/f.vitest.test.mjs
fails vitest-suitefail.txt npx vitest run fail/broken.vitest.test.mjs
fails mocha-fail.txt npx mocha fail/f.mocha.js
fails mocha-hookfail.txt npx mocha fail/hook.mocha.js
fails node-fail.txt node --test fail/f.node.test.js
# node 23+ prints the spec reporter off a TTY too (measured on node 24.22); node
# 22's --test-reporter=spec prints the same tree.
fails node-spec-fail.txt node --test --test-reporter=spec fail/f.node.test.js
fails node-spec-loadfail.txt node --test --test-reporter=spec broken.node.test.js
fails rspec-fail.txt $RSPEC fail/spec/f_spec.rb
fails rspec-loadfail.txt $RSPEC fail/spec/broken_spec.rb
fails minitest-fail.txt ruby fail/f_test.rb
fails phpunit-fail.txt vendor/bin/phpunit fail/tests/FTest.php

# Each skip command names the three failing tests; exactly the one passing test
# must run, and pass.
passed() { grep -oE '([0-9]+ (passed|passing|examples?|runs,))|# pass [0-9]+|OK \([0-9]+ test' | grep -oE '[0-9]+' | head -1; }
skipok() { # skipok <name> <cmd...>
  local name="$1" out rc; shift
  out="$("$@" 2>&1)" && rc=0 || rc=$?
  want "$name-skip" "1 exit 0" "$(printf '%s' "$out" | sed 's/\x1b\[[0-9;]*m//g' | passed) exit $rc"
}
skipok jest npx jest fail/f.jest.test.js --testNamePattern '^(?!(?:Calc adds \(a\+b\)|Calc neg adds|top level)$)'
skipok vitest npx vitest run fail/f.vitest.test.mjs -t '^(?!(?:Calc adds \(a\+b\)|Calc neg adds|top level)$)'
skipok mocha npx mocha fail/f.mocha.js --grep '^(?:Calc adds \(a\+b\)|Calc neg adds|top level)$' --invert
skipok node node --test --test-skip-pattern '^(?:Calc adds \(a\+b\)|Calc neg adds|top level)$' fail/f.node.test.js
# node reads options only before the first file (runner_node.go nodeWith): after it, ignored.
want node-option-after-file-ignored 4 "$(node --test fail/f.node.test.js --test-skip-pattern '^(?:Calc adds \(a\+b\)|Calc neg adds|top level)$' 2>/dev/null | sed -n 's/^# tests //p')"
skipok rspec $RSPEC fail/spec/f_spec.rb -E '\A(?!(?:Calc adds \(a\+b\)|Calc neg adds|MyApp::Calc top level)\z)'
skipok minitest ruby fail/f_test.rb --exclude '/\A(?:CalcTest#test_adds|CalcTest#test_boom|MyApp::NegTest#test_x)\z/'
skipok phpunit vendor/bin/phpunit fail/tests/FTest.php --exclude-filter '/(?:^|\\)(?:FTest::testBoom|FTest::testAdds|FTest::testData)(?: with data set .*)?$/'
skipok npm-test npm test -- fail/f.jest.test.js --testNamePattern '^(?!(?:Calc adds \(a\+b\)|Calc neg adds|top level)$)'
skipok composer-test composer test -- fail/tests/FTest.php --exclude-filter '/(?:^|\\)(?:FTest::testBoom|FTest::testAdds|FTest::testData)(?: with data set .*)?$/'

# --- package scripts: how each launcher hands extra arguments to the script --
# (internal/lang/invocation.go's launcher table; keep the separators equal)
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
YARN=1.22.22 PNPM=9.15.9
jestran() { grep -oE '[0-9]+ passed' | head -1 | cut -d' ' -f1; }
want npm-test-list 5 "$(npm test --silent -- --json --testNamePattern '^\b$' calc.jest.test.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.numTotalTests)')"
want npm-test-single 1 "$(npm test -- --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want npm-run-env-single 1 "$(npm run test:env -- --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want npm-test-bail 0 "$(npm test -- --bail calc.jest.test.js >/dev/null 2>&1; echo $?)"
want yarn-test-single 1 "$(corepack "yarn@$YARN" test --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want pnpm-test-single 1 "$(corepack "pnpm@$PNPM" test -- --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want composer-test-single 1 "$(composer test -- --filter '/(?:^|\\)CalcTest::testAdds(?: with data set .*)?$/' tests/CalcTest.php 2>&1 | sed -n 's/^OK (\([0-9]*\) test.*/\1/p')"

{
  echo "Captured by scripts/capture-runner-fixtures.sh on $(date -u +%Y-%m-%d)."
  echo "jest $JEST, vitest $VITEST, mocha $MOCHA (npm); PHPUnit $PHPUNIT (composer)"
  echo "$(node --version | sed 's/^/node /'); $(ruby -e 'require "rspec/core"; print "rspec-core ", RSpec::Core::Version::STRING')"
  echo "Launchers: npm $(npm --version) and pnpm $PNPM take extra script arguments after --,"
  echo "yarn $YARN takes them directly, composer $(composer --version 2>/dev/null | awk '{print $3}') after --."
  echo "Suites: src/. Each *-list file is the stdout of that runner's list command"
  echo "(*-units files, added 2026-10-25 on node $(node --version): the unit listing under"
  echo "its working-directory prelude, the capture directory written as /fixture)"
  echo "(jest and mocha JSON reduced to the fields the parsers read)."
  echo "Each *-fail, *-suitefail, *-loadfail and *-hookfail file is stdout+stderr of"
  echo "the runner's DEFAULT command on src/fail, not a TTY, as the jail runs it;"
  echo "$(ruby -e 'require "minitest"; print "minitest ", Minitest::VERSION')."
  echo "Go, Ruby, PHP and Rust units and the *-help files (added 2026-10-25): $(go version | awk '{print $3}'),"
  echo "gotestsum $GOTESTSUM (go install), $(php -r 'echo "PHP ", PHP_VERSION;'),"
  echo "$(cargo --version | awk '{print "cargo", $2}'), $(cargo nextest --version 2>/dev/null | head -1)."
  echo "*-help files are each runner's --help; libtest-help.txt is a cargo-built test binary's."
} > "$OUT/PROVENANCE.txt"

exit "$fail"
