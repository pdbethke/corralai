# SPDX-License-Identifier: Elastic-2.0
# SPIKE shared lib: build the tamper-evident hash chain over a run's events, so the
# attestation generator and the ledger tool agree on the head (DRY).

import json, hashlib


def _h(b):
    return hashlib.sha256(b).hexdigest()


def build_chain(events):
    """Return (chain, head): each entry commits to the previous entry's hash."""
    chain = []
    prev = "0" * 64  # genesis
    for i, e in enumerate(events):
        body = {
            "seq": i,
            "ts": e.get("ts"),
            "kind": e.get("kind"),
            "actor": e.get("actor"),
            "model": e.get("model"),
            "subject": e.get("subject"),
            "detail": e.get("detail"),
            "prev": prev,
        }
        entry_hash = _h(json.dumps(body, sort_keys=True).encode())
        chain.append({**body, "hash": entry_hash})
        prev = entry_hash
    head = chain[-1]["hash"] if chain else "0" * 64
    return chain, head


def verify_chain(chain, signed_head):
    prev = "0" * 64
    for entry in chain:
        body = {k: entry[k] for k in ("seq", "ts", "kind", "actor", "model", "subject", "detail", "prev")}
        if entry["prev"] != prev:
            return False, f"broken link at seq {entry['seq']} (prev mismatch)"
        if _h(json.dumps(body, sort_keys=True).encode()) != entry["hash"]:
            return False, f"altered entry at seq {entry['seq']} (hash mismatch)"
        prev = entry["hash"]
    if prev != signed_head:
        return False, "head does not match the ledger's last hash"
    return True, "OK"
