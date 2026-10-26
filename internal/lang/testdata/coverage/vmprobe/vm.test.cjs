// A suite that inspects node:vm, run with and without corral's coverage
// preload (jsCoveragePreload): every observation it makes must be the same.
const assert = require('node:assert');
const { test } = require('node:test');
const vm = require('node:vm');

const map = Buffer.from(JSON.stringify({ version: 3, sources: ['x.ts'], names: [], mappings: 'AAAA' })).toString('base64');
const mapped = (code) => code + '\n//# sourceMappingURL=data:application/json;base64,' + map;

test('runInThisContext returns the value, with or without a map', () => {
  assert.strictEqual(vm.runInThisContext('6 * 7'), 42);
  assert.strictEqual(vm.runInThisContext(mapped('6 * 7'), { filename: __dirname + '/x.js' }), 42);
  assert.strictEqual(vm.runInThisContext(mapped('6 * 7'), __dirname + '/y.js'), 42);
});

test('a thrown error keeps its class, message and identity', () => {
  const boom = new RangeError('boom');
  globalThis.__boom = boom;
  assert.throws(() => vm.runInThisContext(mapped('throw globalThis.__boom'), { filename: __dirname + '/t.js' }), (e) => e === boom);
  assert.throws(() => vm.runInThisContext('(', { filename: __dirname + '/s.js' }), SyntaxError);
  assert.strictEqual(vm.runInThisContext(42), 42);
});

test('a malformed inline map is evaluated exactly as written', () => {
  assert.strictEqual(vm.runInThisContext('1 + 1\n//# sourceMappingURL=data:application/json;base64,@@@', { filename: __dirname + '/m.js' }), 2);
});

test('Script: instanceof, subclassing, new.target and its methods', () => {
  const s = new vm.Script(mapped('this.kind = "script"; 7'), { filename: __dirname + '/z.js' });
  assert.ok(s instanceof vm.Script);
  assert.strictEqual(s.runInThisContext(), 7);
  assert.strictEqual(globalThis.kind, 'script');
  class Sub extends vm.Script { tag() { return 'sub'; } }
  const sub = new Sub('8');
  assert.ok(sub instanceof Sub && sub instanceof vm.Script);
  assert.strictEqual(sub.tag(), 'sub');
  assert.strictEqual(sub.runInNewContext(), 8);
  assert.strictEqual(Object.getPrototypeOf(sub).constructor, Sub);
  assert.throws(() => vm.Script('1'), TypeError);
  assert.throws(() => new vm.Script(')'), SyntaxError);
});

test('compileFunction keeps its parameters and result', () => {
  const f = vm.compileFunction(mapped('return a + b'), ['a', 'b'], { filename: __dirname + '/f.js' });
  assert.strictEqual(f(2, 3), 5);
  assert.strictEqual(f.length, 2);
});

test('names and lengths read as Node defines them', () => {
  assert.deepStrictEqual(
    [vm.runInThisContext.name, vm.runInThisContext.length, vm.compileFunction.name, vm.compileFunction.length, vm.Script.name, vm.Script.length],
    ['runInThisContext', 2, 'compileFunction', 2, 'Script', 1]);
});

test('an fs spy installed by the suite sees no write it did not make', () => {
  const fs = require('node:fs');
  const real = fs.appendFileSync, realMkdir = fs.mkdirSync;
  let calls = 0;
  fs.appendFileSync = function () { calls++; return Reflect.apply(real, this, arguments); };
  fs.mkdirSync = function () { calls++; return Reflect.apply(realMkdir, this, arguments); };
  try {
    assert.strictEqual(vm.runInThisContext(mapped('5'), { filename: __dirname + '/spy.js' }), 5);
    assert.strictEqual(new vm.Script(mapped('6'), { filename: __dirname + '/spy2.js' }).runInThisContext(), 6);
  } finally {
    fs.appendFileSync = real;
    fs.mkdirSync = realMkdir;
  }
  assert.strictEqual(calls, 0);
});

test('Script keeps its constructor identity and prototype', () => {
  const s = new vm.Script('1');
  assert.strictEqual(s.constructor, vm.Script);
  assert.strictEqual(vm.Script.prototype.constructor, vm.Script);
  assert.strictEqual(Object.getPrototypeOf(s), vm.Script.prototype);
});

test('String() of the vm functions reads as Node defines them', () => {
  assert.ok(String(vm.Script).startsWith('class Script extends ContextifyScript {'));
  assert.ok(String(vm.runInThisContext).startsWith('function runInThisContext(code, options)'));
  assert.ok(String(vm.compileFunction).startsWith('function compileFunction(code, params, o'));
});

test('option getters are read as often as vm itself reads them', () => {
  let reads = 0;
  vm.runInThisContext(mapped('1'), { get filename() { reads++; return __dirname + '/g.js'; }, lineOffset: 0 });
  assert.strictEqual(reads, 1);
  let offsets = 0;
  vm.runInThisContext(mapped('1'), { filename: __dirname + '/h.js', get lineOffset() { offsets++; return 0; }, get columnOffset() { offsets++; return 0; } });
  assert.strictEqual(offsets, 2);
  let scriptReads = 0;
  new vm.Script(mapped('1'), { get filename() { scriptReads++; return __dirname + '/i.js'; } });
  const plainScript = (() => { let n = 0; new vm.Script('1', { get filename() { n++; return __dirname + '/j.js'; } }); return n; })();
  assert.strictEqual(scriptReads, plainScript);
});
