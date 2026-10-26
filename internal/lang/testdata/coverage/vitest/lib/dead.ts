// Imported by the suite and never called: every function line is measured
// and none ran.
export type Mode = 'loud' | 'quiet';

export function shout(s: string, mode: Mode): string {
  if (mode === 'loud') {
    return s.toUpperCase();
  }
  return s;
}
