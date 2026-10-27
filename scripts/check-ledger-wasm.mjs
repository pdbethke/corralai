// Derive a ledger directory through ledger.wasm and print what it concluded.
// usage: node check-ledger-wasm.mjs <wasmdir> <ledgerdir> <pubhex> <owner/name> <outfile>
// Writes the wasm's full Derived JSON to <outfile>, exactly the string it
// returned, so the shell wrapper can compare it byte for byte with the native
// Derive (scripts/ledgerderive). Prints one JSON object:
// {problem, entries, verified, unsigned, failed, scans}.
// Exit 1 on a ledger-level problem or any failed entry; the shell wrapper
// compares verified against the CLI's count.
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

const [wasmDir, ledgerDir, pub, repo, outFile] = process.argv.slice(2);
if (!wasmDir || !ledgerDir || pub === undefined || !repo || !outFile) {
  console.error("usage: check-ledger-wasm.mjs <wasmdir> <ledgerdir> <pubhex> <owner/name> <outfile>");
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
// Files in name order, the order the native reader (auditpush.ReadRawLedgerDir)
// hands them over; Derive sorts by name itself, so this is belt and braces.
entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
const out = globalThis.corralLedgerDerive(entries, { rootPub: pub, repo });
fs.writeFileSync(outFile, out);
const d = JSON.parse(out);
const n = s => d.entries.filter(e => e.status === s).length;
console.log(JSON.stringify({
  problem: d.problem ?? "", entries: d.entries.length,
  verified: n("verified"), unsigned: n("unsigned"), failed: n("failed"), scans: d.scans.length,
}));
process.exit(d.problem || n("failed") ? 1 : 0);
