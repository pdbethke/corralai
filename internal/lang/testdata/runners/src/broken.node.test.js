// Fixture: a node:test file that throws while loading, so its listing run
// reports the FILE as a failed test (scripts/capture-runner-fixtures.sh).
const { test } = require('node:test');
throw new Error('this file does not load');
test('never registered', () => {});
