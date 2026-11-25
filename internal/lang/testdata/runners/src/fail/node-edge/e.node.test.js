// Edge fixture for the node --test failure hook (scripts/capture-runner-fixtures.sh): shapes where node's JUnit record and its printed report could disagree.
const {describe,it,test,before} = require('node:test'); const assert = require('assert');
test('parent', async (t) => { await t.test('child fails', () => { assert.equal(1, 2); }); });
test('body fails after child', async (t) => { await t.test('child ok', () => {}); throw new Error('after'); });
describe('hooked', () => { before(() => { throw new Error('hook'); }); it('a', () => {}); });
test.todo('todo fails', () => { throw new Error('todo'); });
test('a > b :: c #1', () => { assert.equal(1, 2); });
