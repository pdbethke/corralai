# SPDX-License-Identifier: Elastic-2.0
# SPIKE (throwaway): map a real corralai run recording -> an in-toto Statement v1
# carrying an SLSA Provenance v1 predicate, enriched with corralai's accountability
# evidence (execution-certification, the human gate, findings, per-task model
# attribution). Purpose: SEE what an "AI accountability" attestation looks like.
#
# Usage: python3 gen.py <recording.json> <recording.meta.json> > attestation.json

import json, sys, hashlib, datetime
from chainlib import build_chain

rec_path, meta_path = sys.argv[1], sys.argv[2]
events = json.load(open(rec_path))["events"]
meta = json.load(open(meta_path))


def iso(ts):
    return datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).isoformat()


def by(kind):
    return [e for e in events if e.get("kind") == kind]


ts_all = [e["ts"] for e in events if "ts" in e]
started, finished = min(ts_all), max(ts_all)

# Subject digest = the HEAD of the tamper-evident, hash-linked build ledger
# (see ledger.py). Binds this attestation to the signed, ordered record of every
# step — you can't alter a step without breaking the head this attestation names.
_chain, run_digest = build_chain(events)

# --- the differentiator: execution-certification evidence (the verify gate ran) ---
certifications = [
    {
        "command": e["subject"],
        "exitCode": e["detail"].get("exit_code"),
        "passed": e["detail"].get("ok", False),
        "byModel": e.get("model"),
        "byAgent": e.get("actor"),
        "role": e["detail"].get("role"),
        "at": iso(e["ts"]),
    }
    for e in by("execution")
]

# --- human gate: who accepted, when ---
reviews = [{"acceptedBy": e.get("actor"), "at": iso(e["ts"])} for e in by("review_accepted")]

# --- findings: raised + terminal disposition ---
reported = by("finding_reported")
resolved = by("finding_resolved")
sev = {}
for e in reported:
    s = e["detail"].get("severity", "?")
    sev[s] = sev.get(s, 0) + 1

# --- per-task model attribution (who/what did each task) ---
attribution = [
    {"task": e.get("subject"), "model": e.get("model"), "backend": e["detail"].get("backend"), "agent": e.get("actor")}
    for e in by("task_completed")
]

models = meta.get("models", [])

statement = {
    "_type": "https://in-toto.io/Statement/v1",
    "subject": [
        {
            "name": "mission:calc-frontier (Go module 'calc')",
            "digest": {"sha256": run_digest},  # binds to the exact recorded run
        }
    ],
    "predicateType": "https://slsa.dev/provenance/v1",
    "predicate": {
        "buildDefinition": {
            "buildType": "https://corralai.dev/mission-run/v1",
            "externalParameters": {
                "directive": meta.get("directive"),
                "requiresHumanReview": len(reviews) > 0,
            },
            "internalParameters": {
                "engine": "corralai-brain",
                "correctnessGate": "certify-by-execution (deterministic; a judge may not certify herself)",
                "platform": meta.get("platform"),
            },
            # Models are MATERIALS — the provenance names every model that produced the change.
            "resolvedDependencies": [{"uri": "model:" + m} for m in models],
        },
        "runDetails": {
            "builder": {
                "id": "https://corralai.dev/brain",
                "builderDependencies": [{"uri": "model:" + m} for m in models],
            },
            "metadata": {
                "invocationId": "mission:calc-frontier",
                "startedOn": iso(started),
                "finishedOn": iso(finished),
            },
            # corralai accountability evidence lives in byproducts (each a
            # ResourceDescriptor; structured data carried in annotations for legibility).
            "byproducts": [
                {
                    "name": "accountability/tamper-evident-ledger",
                    "mediaType": "application/vnd.corralai.build-ledger+json",
                    "digest": {"sha256": run_digest},  # = the ledger head (this attestation's subject)
                    "annotations": {
                        "note": "signed hash-linked ledger of every build step (git/CT/Rekor-style); "
                        "altering any step breaks this head. See <slug>.ledger.json.",
                        "steps": len(events),
                    },
                },
                {
                    "name": "certification/execution",
                    "mediaType": "application/vnd.corralai.certification+json",
                    "annotations": {
                        "summary": f"{sum(1 for c in certifications if c['passed'])}/{len(certifications)} recorded checks passed",
                        "checks": certifications,
                    },
                },
                {
                    "name": "accountability/human-gate",
                    "annotations": {"reviews": reviews, "note": "output withheld until a human accepted"},
                },
                {
                    "name": "accountability/findings",
                    "annotations": {
                        "raised": len(reported),
                        "resolved": len(resolved),
                        "bySeverity": sev,
                    },
                },
                {
                    "name": "accountability/attribution",
                    "annotations": {
                        "note": "every task tied to the model + agent that produced it",
                        "tasks": attribution,
                    },
                },
            ],
        },
    },
}

print(json.dumps(statement, indent=2))
