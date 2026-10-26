// The named ESM exports of node:vm are the same functions the preload wraps
// (module.syncBuiltinESMExports), and behave as Node's own.
import assert from 'node:assert';
import { test } from 'node:test';
import vm, { runInThisContext, Script } from 'node:vm';

test('named and default exports agree and evaluate', () => {
  assert.strictEqual(runInThisContext, vm.runInThisContext);
  assert.strictEqual(Script, vm.Script);
  assert.strictEqual(runInThisContext('2 ** 5'), 32);
  assert.strictEqual(new Script('3').runInThisContext(), 3);
});
