// Report fixture (scripts/capture-runner-fixtures.sh): a failing test run under a jest configuration that asks for a report file of the project's own (theirs.json); the failure hook must never write it.
describe('Calc', () => {
  it('adds (a+b)', () => { expect(1 + 1).toBe(3); });
  it('ok', () => {});
  describe('neg', () => { it('adds', () => { throw new Error('boom'); }); });
});
test('top level', () => { expect(1).toBe(2); });
