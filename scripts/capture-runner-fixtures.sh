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
npm install --silent --no-audit --no-fund "jest@$JEST" "vitest@$VITEST" "mocha@$MOCHA" >/dev/null
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
vendor/bin/phpunit --list-tests tests/CalcTest.php > "$OUT/phpunit-list.txt"

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
  echo "(jest and mocha JSON reduced to the fields the parsers read)."
} > "$OUT/PROVENANCE.txt"

exit "$fail"
