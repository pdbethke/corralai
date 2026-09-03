const assert = require('node:assert');
const { test } = require('node:test');
const calc = require('../lib/calc');
require('../lib/dead');

test('add', () => { assert.strictEqual(calc.add(1, 2), 3); });
