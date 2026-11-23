// Report fixture (scripts/capture-runner-fixtures.sh): a project config that hides the lines corral reads, and the runner row's report flags that bring them back.
const assert = require('assert');
describe('Calc', () => {
  it('adds (a+b)', () => { assert.equal(1 + 1, 3); });
  it('ok', () => {});
  describe('neg', () => { it('adds', () => { throw new Error('boom'); }); });
});
it('top level', () => { assert.equal(1, 2); });
