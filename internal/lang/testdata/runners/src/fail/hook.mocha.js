// A failing hook: mocha names the hook, not a test, so nothing is named.
describe('Hooked', () => {
  before(() => { throw new Error('hook boom'); });
  it('never runs', () => {});
});
