// Failing fixture for the jest row's failure hook (scripts/capture-runner-fixtures.sh): the shapes where jest's JSON record and its printed `●` headings could disagree.
describe('Sep', () => { it('a › b', () => { expect(1).toBe(2); }); });
describe('BeforeAll', () => {
  beforeAll(() => { throw new Error('setup'); });
  it('one', () => {});
});
describe('AfterAll', () => {
  afterAll(() => { throw new Error('teardown'); });
  it('two', () => {});
});
test.todo('later');
test.failing('fails as expected', () => { expect(1).toBe(2); });
test.failing('passes unexpectedly', () => {});
test.each([1, 2])('each %i', (n) => { expect(n).toBe(1); });
