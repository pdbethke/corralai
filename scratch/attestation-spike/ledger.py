# SPDX-License-Identifier: Elastic-2.0
# SPIKE: emit a tamper-evident, hash-linked, signed ledger of a run's build steps
# (like git / Certificate Transparency / Sigstore Rekor), and prove that altering
# any past step breaks verification. Writes <slug>.ledger.json next to the input.
#
# Usage: python3 ledger.py <recording.json>
# NOTE: real signing = DSSE/cosign/Sigstore over the head; HMAC here just shows shape.

import json, sys, os, hashlib, hmac, copy
from chainlib import build_chain, verify_chain

rec_path = sys.argv[1]
events = json.load(open(rec_path))["events"]
DEMO_KEY = b"demo-signing-key-swap-for-cosign"

chain, head = build_chain(events)
signature = hmac.new(DEMO_KEY, head.encode(), hashlib.sha256).hexdigest()

# write the ledger beside this script, regardless of cwd
out = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                   os.path.basename(rec_path).replace(".json", ".ledger.json"))
json.dump({
    "ledgerType": "https://corralai.dev/build-ledger/v1",
    "algorithm": "sha256 hash-chain; head signed",
    "head": head,
    "signature": {"scheme": "HMAC-SHA256 (demo; production: DSSE/cosign)", "value": signature},
    "steps": chain,
}, open(out, "w"), indent=2)

print(f"ledger: {len(chain)} build steps, hash-linked  ->  {out}")
for entry in chain[:3]:
    print(f"  seq {entry['seq']:>2} {entry['kind']:<16} hash={entry['hash'][:12]}…  prev={entry['prev'][:12]}…")
print(f"signed head: {head[:16]}…   signature(HMAC-demo): {signature[:16]}…")

ok, msg = verify_chain(chain, head)
print(f"\nverify untampered  -> {ok}  ({msg})")

tampered = copy.deepcopy(chain)
for entry in tampered:
    if entry["kind"] == "execution" and entry.get("detail", {}).get("ok") is True:
        entry["detail"] = {**entry["detail"], "ok": False, "exit_code": 1}
        print(f"\n[tamper] rewrote seq {entry['seq']} execution ok:true -> false (did NOT recompute the chain)")
        break
ok2, msg2 = verify_chain(tampered, head)
print(f"verify tampered    -> {ok2}  ({msg2})")
