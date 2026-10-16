// Fixture for the runner table's node --test row (scripts/capture-runner-fixtures.sh).
const { describe, it, test } = require('node:test');
const assert = require('node:assert');
describe('Calc', () => {
  it('adds (a+b)', () => { assert.strictEqual(1 + 1, 2); });
  it('adds', () => {});
  describe('neg', () => { it('adds', () => {}); });
});
test('top level', () => {});
test('Calc adds', () => {});
