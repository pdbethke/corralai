// Fixture for the runner table's multi-unit commands (scripts/capture-runner-fixtures.sh): a holds one test, b two, c four, so a run's count names the set it ran.
const { test } = require('node:test');
test('c one', () => {});
test('c two', () => {});
test('c three', () => {});
test('c four', () => {});
