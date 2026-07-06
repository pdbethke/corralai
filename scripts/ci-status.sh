#!/usr/bin/env bash
set -euo pipefail

if ! command -v gh >/dev/null 2>&1; then
  echo "CI status unavailable: GitHub CLI (gh) is not installed." >&2
  exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "CI status unavailable: gh is not authenticated. Run: gh auth login" >&2
  exit 1
fi

head_sha=$(git rev-parse HEAD)
short_sha=$(git rev-parse --short HEAD)
branch=$(git rev-parse --abbrev-ref HEAD)

run_for_head_json=$(gh run list --branch "$branch" --commit "$head_sha" --limit 1 --json workflowName,status,conclusion,url,headSha)

parse_run() {
  python3 -c 'import json,sys
runs=json.loads(sys.argv[1])
if not runs:
    sys.exit(1)
r=runs[0]
vals=[r.get("workflowName") or "unknown-workflow", r.get("status") or "unknown", r.get("conclusion") or "pending", r.get("url") or ""]
print("\t".join(vals))' "$1"
}

if run_line=$(parse_run "$run_for_head_json" 2>/dev/null); then
  IFS=$'\t' read -r workflow status conclusion url <<<"$run_line"
  echo "CI ${branch}@${short_sha}: ${workflow} ${status}/${conclusion} ${url}"
  exit 0
fi

run_for_branch_json=$(gh run list --branch "$branch" --limit 1 --json workflowName,status,conclusion,url,headSha)
if run_line=$(python3 -c 'import json,sys
runs=json.loads(sys.argv[1])
if not runs:
    sys.exit(1)
r=runs[0]
vals=[(r.get("headSha") or "")[:7], r.get("workflowName") or "unknown-workflow", r.get("status") or "unknown", r.get("conclusion") or "pending", r.get("url") or ""]
print("\t".join(vals))' "$run_for_branch_json" 2>/dev/null); then
  IFS=$'\t' read -r run_short workflow status conclusion url <<<"$run_line"
  echo "CI ${branch}@${short_sha}: no run yet for HEAD; latest ${run_short} ${workflow} ${status}/${conclusion} ${url}"
  exit 0
fi

echo "CI ${branch}@${short_sha}: no workflow runs found yet."
