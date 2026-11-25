// Fixture (scripts/capture-runner-fixtures.sh): where vitest writes a JUnit report corral would ask for on the command line, under a project config of this shape.
import {test,expect} from 'vitest';
test('passes', () => { expect(1).toBe(1); });
