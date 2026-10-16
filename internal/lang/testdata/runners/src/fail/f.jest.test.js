// Failing fixture for the runner table (scripts/capture-runner-fixtures.sh): what each runner prints when tests fail, and which tests its skip command leaves.
describe('Calc', () => {
  it('adds (a+b)', () => { expect(1 + 1).toBe(3); });
  it('ok', () => {});
  describe('neg', () => { it('adds', () => { throw new Error('boom'); }); });
});
test('top level', () => { expect(1).toBe(2); });
