// A file that fails to load: no test in it can be named.
import {describe,it} from 'vitest';
describe('Broken', () => {
  it('x', () => {});
