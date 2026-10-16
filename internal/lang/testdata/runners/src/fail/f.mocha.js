// Failing fixture for the runner table (scripts/capture-runner-fixtures.sh): what each runner prints when tests fail, and which tests its skip command leaves.
const assert = require('assert');
describe('Calc', () => {
  it('adds (a+b)', () => { assert.equal(1 + 1, 3); });
  it('ok', () => {});
  describe('neg', () => { it('adds', () => { throw new Error('boom'); }); });
});
it('top level', () => { assert.equal(1, 2); });
