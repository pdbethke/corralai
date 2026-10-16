// Fixture for the runner table's vitest row (scripts/capture-runner-fixtures.sh).
import { describe, it, test, expect } from 'vitest';
describe('Calc', () => {
  it('adds (a+b)', () => { expect(1 + 1).toBe(2); });
  it('adds', () => {});
  describe('neg', () => { it('adds', () => {}); });
});
test('top level', () => {});
test('Calc adds', () => {});
