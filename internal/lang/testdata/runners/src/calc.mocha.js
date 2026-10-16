// Fixture for the runner table's mocha row (scripts/capture-runner-fixtures.sh).
describe('Calc', () => {
  it('adds (a+b)', () => { if (1 + 1 !== 2) throw new Error('sum'); });
  it('adds', () => {});
  describe('neg', () => { it('adds', () => {}); });
});
it('top level', () => {});
it('Calc adds', () => {});
