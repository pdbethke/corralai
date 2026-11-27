// Fail-fast fixture for the runner table (scripts/capture-runner-fixtures.sh): each of the two files fails one test.
import {test,expect} from "vitest";
test("a fails", () => { expect(1).toBe(2); });
