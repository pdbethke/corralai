// Exercised by the suite through vitest, which runs TRANSFORMED code: the
// type annotations below are stripped, so V8's offsets are not this file's.
export interface Pair { a: number; b: number }

export function add(p: Pair): number {
  const total: number = p.a + p.b;
  return total;
}

export function sub(p: Pair): number {
  const diff: number = p.a - p.b;
  return diff;
}
