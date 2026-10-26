// SPDX-License-Identifier: Elastic-2.0
// site/scripts/build-ledger-wasm.mjs
//
// Builds the results page's verifier, ledger.Derive compiled to WebAssembly,
// into public/results/wasm/ from THIS checkout (scripts/build-ledger-wasm.sh),
// so the page and the verifier it runs always come from one commit. Runs as
// part of prebuild and predev; needs Go on PATH. A failed build fails the
// site build: a results page without its verifier has nothing true to show.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const SITE_ROOT = path.join(import.meta.dirname, '..');
// Absolute: the shell script runs from the repository root.
const OUT = path.join(SITE_ROOT, 'public', 'results', 'wasm');

// The directory is wholly derived, so it is emptied first: nothing stale
// survives a rebuild, and wasm_exec.js (copied with GOROOT's read-only mode)
// does not block the next copy over it.
fs.rmSync(OUT, { recursive: true, force: true });
execFileSync('bash', [path.join(SITE_ROOT, '..', 'scripts', 'build-ledger-wasm.sh'), OUT], { stdio: 'inherit' });
