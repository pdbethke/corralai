// SPDX-License-Identifier: Elastic-2.0
// Password strength validator.
//
// valid(p) is true iff p is at least 12 characters long AND contains an
// uppercase letter, a lowercase letter, a digit, and a symbol.
export function valid(p: string): boolean {
  if (p.length < 12) return false;
  let up = false, lo = false, di = false, sy = false;
  for (const c of p) {
    if (/[A-Z]/.test(c)) up = true;
    else if (/[a-z]/.test(c)) lo = true;
    else if (/[0-9]/.test(c)) di = true;
    else if (/[^A-Za-z0-9]/.test(c)) sy = true;
  }
  return up && lo && di && sy;
}
