// Fixture for the runner table's node --test unit rows (scripts/capture-runner-fixtures.sh).
// An only test: under --test-only node reports it in place of this file's path.
const { test } = require('node:test');
test.only('c only', () => {});
test('c two', () => {});
