// SPDX-License-Identifier: Elastic-2.0
// The 'gappy' suite: it only ever feeds a VALID password (and one too-short
// one), so it never exercises the four character-class rules — a mutant that
// drops, say, the digit requirement passes it undetected.
import { test } from "node:test";
import assert from "node:assert";
import { valid } from "./passwd.ts";

test("valid length", () => {
  assert.ok(valid("Abcdefgh1!xy"));
});

test("too short", () => {
  assert.ok(!valid("Ab1!xyz"));
});
