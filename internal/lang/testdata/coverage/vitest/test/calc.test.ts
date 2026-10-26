import { add } from '../lib/calc.ts';
import '../lib/dead.ts';

test('add', () => {
  expect(add({ a: 1, b: 2 })).toBe(3);
});
