import assert from 'node:assert';
import { test } from 'node:test';
import { add } from '../lib/calc.ts';
import '../lib/dead.ts';

test('add', () => { assert.strictEqual(add(1, 2), 3); });
