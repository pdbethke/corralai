// Report fixture (scripts/capture-runner-fixtures.sh): a project config that hides the lines corral reads, and the runner row's report flags that bring them back.
import {describe,it,test,expect} from "vitest";
describe('Calc', () => {
  it('adds (a+b)', () => { expect(1 + 1).toBe(3); });
  it('ok', () => {});
  describe('neg', () => { it('adds', () => { throw new Error('boom'); }); });
});
test('top level', () => { expect(1).toBe(2); });
