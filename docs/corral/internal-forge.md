<!-- SPDX-License-Identifier: Elastic-2.0 -->

# corral on an internal forge — GitLab, Gitea, Bitbucket, Gerrit

The GitHub Action is a convenience, not a dependency. Everything it does is
one `corral certify --repo` invocation plus a git branch, and both work on
any forge — including one that nothing outside your network can reach.
That is the point for an organization with an internal forge: **the record
never leaves the perimeter.** Every scan's entry is a signed, hash-linked
file in a branch of the repository it audits; it is verified with a public
key you hold; DuckDB reads it in place on any machine that can clone the
repo. There is no warehouse to stand up and no service to trust.

Two things to state exactly, because the docs on the public forge state
them and yours should too:

- **The record stays in; the model calls do not**, unless every seat is a
  local model. corral names no model of its own — you name each seat —
  so the perimeter question is answered by which endpoints you name: an
  Ollama daemon inside the network (`--local-endpoint`), a gateway you
  run, or a vendor. What goes out to a vendor is the file under audit and
  the tests around it, the same bytes a developer pastes into a chat.
- **There is no keyless attestation.** On GitHub, `actions/attest` signs
  each entry with the workflow's OIDC identity and no key. Off GitHub the
  chain's trust rests on the **certify key** you configure
  (`CORRALAI_CERTIFY_KEY`, a 32-byte Ed25519 seed as hex — or
  `CORRALAI_CERTIFY_KEY_FILE` naming a file holding the same): every entry
  is signed with it, `corral verify --ledger <dir> --pub <hex>` checks every
  signature against its public half, and custody of that seed is the
  thing your process has to get right. Keep it in the CI secret store,
  never in the repository; rotate it by adding the new public key to the
  verifier's list, not by rewriting entries. GitLab's own OIDC ID tokens
  work with `cosign sign-blob` if you want a keyless witness as well.

## The recipe, forge-neutral

What the Action does, as shell:

```bash
# 1. The ledger branch, checked out into a worktree.
if git ls-remote --exit-code --heads origin corral/ledger >/dev/null 2>&1; then
  git fetch --no-tags origin corral/ledger
  git worktree add .corral-ledger origin/corral/ledger
else
  git worktree add --detach .corral-ledger
  (cd .corral-ledger && git checkout --orphan corral/ledger && git rm -rfq . \
     && echo "corral's record — see docs/corral/internal-forge.md" > README.md)
fi

# 2. The audit. The entry is written into the worktree; earlier entries
#    there are read as the prior.
corral certify --repo "$PWD" --substrate workspace \
  --owner "$OWNER" --commit "$COMMIT_SHA" \
  --diff-base "origin/$TARGET_BRANCH" \
  --ledger .corral-ledger --record-mutants ".corral-ledger/mutants/$COMMIT_SHA.json" \
  --derive-model gemini-3.6-flash --mutant-model gemini-3.6-flash \
  --writer-model gemini-3.6-flash --critic-model claude-haiku-4-5 \
  --min-kill-rate 0.6 \
  -- pytest -q
code=$?

# 3. Commit the entry back. A chain is one writer at a time: if another
#    run pushed first, re-LINK the entry (`corral ledger append`) rather
#    than rebasing — a rebase moves the commit, not the link.
cd .corral-ledger
git add -A && git -c user.name=corral -c user.email=corral@noreply commit -qm "corral: $COMMIT_SHA" || exit "$code"
for i in 1 2 3; do
  git push origin HEAD:corral/ledger && exit "$code"
  ours=$(git diff --name-only HEAD~1 HEAD -- scans/ | head -1)
  cp "$ours" /tmp/entry.json.gz
  git fetch origin corral/ledger && git reset -q --hard origin/corral/ledger
  corral ledger append /tmp/entry.json.gz . && git add -A \
    && git -c user.name=corral -c user.email=corral@noreply commit -qm "corral: $COMMIT_SHA (re-linked)"
done
exit 1
```

Skip step 3 on a merge request from a fork, for the same reason the Action
skips it on `pull_request`: a stranger's change must not write the
repository's record.

## GitLab

`.gitlab-ci.yml`, one job. The two GitLab-specific facts: `CI_JOB_TOKEN`
**cannot push** by default, so the push uses a project access token with
`write_repository` (masked variable `CORRAL_LEDGER_TOKEN`); and the
merge-request target is `CI_MERGE_REQUEST_TARGET_BRANCH_NAME`.

```yaml
corral:
  image: golang:1.26
  variables:
    GIT_DEPTH: 0
    # CORRALAI_CERTIFY_KEY (hex seed), CORRAL_LEDGER_TOKEN, GEMINI_API_KEY and
    # ANTHROPIC_API_KEY are masked CI/CD variables, never in this file.
  before_script:
    - go install github.com/pdbethke/corralai/cmd/corral@v1.0.0-rc.2
    - export PATH="$PATH:$(go env GOPATH)/bin"
    - git remote set-url origin "https://oauth2:${CORRAL_LEDGER_TOKEN}@${CI_SERVER_HOST}/${CI_PROJECT_PATH}.git"
  script:
    - OWNER="$CI_PROJECT_NAMESPACE" COMMIT_SHA="$CI_COMMIT_SHA"
      TARGET_BRANCH="${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-$CI_DEFAULT_BRANCH}"
      bash ci/corral.sh          # the recipe above, verbatim
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
```

The verdict goes to the job log; corral prints it verbatim, and the exit
code is the gate — make the job required in the merge request settings.
`corral certify --repo … --attest statement.json` writes the same in-toto
statement the Action attests, signed with the certify key into a DSSE
envelope beside it; `corral verify --attest statement.json --pub <hex>`
checks it anywhere.

## Gitea, Bitbucket, Gerrit

The recipe is the same shell; only the push credential and the
target-branch variable change. Gitea Actions runs GitHub-style workflows,
so the Action's own YAML works there with `uses: pdbethke/corralai@v1.0.0-rc.2`
pointed at a mirror (Gitea resolves `uses:` against `github.com` by
default) and `actions/attest` removed — there is no attestation API to
call. Bitbucket Pipelines: `BITBUCKET_PR_DESTINATION_BRANCH` is the target,
and an app password or access token with repository write is the push
credential. Gerrit: the ledger branch is an ordinary ref; push it with the
job's own credential, and the `corral/ledger` ref needs no review — it is
the record, not a change.

## Reading it back, inside

`git worktree add .corral/ledger corral/ledger` on any clone, then
`corral scans list`, `corral scans show <id>`, `corral verify --ledger
.corral/ledger --pub <hex>`, or plain DuckDB:

```sql
SELECT e.bundle.Scan.Commit[1:8] AS commit, f.Path, f.KillRate, f.ProvenMissed
FROM read_json_auto('.corral/ledger/scans/*.json.gz') AS e, unnest(e.bundle.Files) AS t(f)
WHERE f.Disposition = 'audited' ORDER BY e.pushed DESC;
```

Nothing in that query left the building.
