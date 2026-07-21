// SPDX-License-Identifier: Elastic-2.0
// The 'gappy' suite: it only ever feeds a VALID password (and one too-short
// one), so it never exercises the four character-class rules — a mutant that
// drops, say, the digit requirement passes it undetected.
const { test } = require("node:test");
const assert = require("node:assert");
const { valid } = require("./passwd.js");

test("valid length", () => {
  assert.ok(valid("Abcdefgh1!xy"));
});

test("too short", () => {
  assert.ok(!valid("Ab1!xyz"));
});
