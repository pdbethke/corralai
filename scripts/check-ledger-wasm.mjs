// Derive a ledger directory through ledger.wasm and print what it concluded.
// usage: node check-ledger-wasm.mjs <wasmdir> <ledgerdir> <pubhex>
// Prints one JSON object: {problem, entries, verified, unsigned, failed, scans}.
// Exit 1 on a ledger-level problem or any failed entry; the shell wrapper
// compares verified against the CLI's count.
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

const [wasmDir, ledgerDir, pub] = process.argv.slice(2);
if (!wasmDir || !ledgerDir || pub === undefined) {
  console.error("usage: check-ledger-wasm.mjs <wasmdir> <ledgerdir> <pubhex>");
  process.exit(2);
}
// wasm_exec.js is a classic script that populates globalThis.
vm.runInThisContext(fs.readFileSync(path.join(wasmDir, "wasm_exec.js"), "utf8"));
const go = new Go();
const { instance } = await WebAssembly.instantiate(
  fs.readFileSync(path.join(wasmDir, "ledger.wasm")), go.importObject);
go.run(instance); // blocks on select{} in Go; registration is synchronous before that

// The same files auditpush.ReadLedgerDir reads: .json / .json.gz under scans/,
// no dotfiles, no subdirectories.
const scans = path.join(ledgerDir, "scans");
const entries = fs.existsSync(scans)
  ? fs.readdirSync(scans, { withFileTypes: true })
      .filter(e => e.isFile() && !e.name.startsWith(".") &&
                   (e.name.endsWith(".json") || e.name.endsWith(".json.gz")))
      .map(e => ({ name: e.name, bytes: new Uint8Array(fs.readFileSync(path.join(scans, e.name))) }))
  : [];
const d = JSON.parse(globalThis.corralLedgerDerive(entries, { rootPub: pub, repo: "" }));
const n = s => d.entries.filter(e => e.status === s).length;
console.log(JSON.stringify({
  problem: d.problem ?? "", entries: d.entries.length,
  verified: n("verified"), unsigned: n("unsigned"), failed: n("failed"), scans: d.scans.length,
}));
process.exit(d.problem || n("failed") ? 1 : 0);
