#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-runner-fixtures.sh [--keep] [DIR] — runs the real jest,
# vitest, mocha, node --test, rspec and phpunit against the fixture suites in
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
# Needs the network (npm, composer) and node, ruby with rspec, php. DIR
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
  sh -c "$PRELUDE" corral-units "$@" 2>/dev/null | sed -e "s#$PDIR#/fixture#g" -e '/duration_ms/d' > "$OUT/$f" || true
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
# A shard of one file can be empty, and an empty run passes; UnitCmd drops it.
want node-shard-of-one-file-is-empty "0 exit 0" "$(out=$(node --test --test-reporter=tap --test-shard=2/2 units/node/a.node.test.js 2>&1) && echo "$(printf '%s' "$out" | unitran) exit 0")"
want jest-runTestsByPath-adds-named-file 3 "$(npx --no-install jest units/jest/b.jest.test.js --runTestsByPath units/jest/a.jest.test.js 2>&1 | unitran)"
want vitest-unit 2 "$(npx --no-install vitest run --reporter=verbose units/vitest/a.vitest.test.mjs 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
want vitest-unit-substring-collision 3 "$(npx --no-install vitest run --root units/collide units/collide/a.vitest.test.mjs 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
want node-unit 2 "$(node --test --test-reporter=tap --test-concurrency 1 units/node/a.node.test.js 2>&1 | unitran)"
want mocha-config-spec-merges 2 "$(cd units/mocharc && npx --no-install mocha t/a.mocha.js 2>&1 | unitran)"

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
} > "$OUT/PROVENANCE.txt"

exit "$fail"
