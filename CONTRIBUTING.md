# Contributing to CorralAI

CorralAI is **source-available** under the [Elastic License 2.0](LICENSE):
read it, modify it, self-host it. The one thing you can't do is offer it to
third parties as a hosted or managed service — that path is available under a
commercial license (contact licensing@corralai.dev).

## Contributor License Agreement

CorralAI runs a dual-license model (ELv2 for everyone + a commercial license for
hosted-service use). For contributions to flow into both, we need a one-time
**Contributor License Agreement** from each contributor. You keep ownership of
your work; you grant the maintainer the right to license it under both.

The first time you open a pull request, a bot will ask you to sign by commenting:

> I have read the CLA Document and I hereby sign the CLA

The full text is in [CLA.md](CLA.md). It's a one-time signature covering all your
future contributions.

## Workflow

1. Open an issue or discussion for non-trivial changes first.
2. `go build ./...`, `go vet ./...`, and `go test ./...` must pass.
3. New `.go` files must carry the SPDX header — run `bash scripts/add-spdx.sh`.
4. `bash scripts/check-licensing.sh` must exit 0.
5. `gofmt -l ./cmd ./internal` must print nothing, and
   `bash scripts/check-security.sh` must exit 0 — both are CI gates.
6. Merge through a pull request. Branch protection sets `enforce_admins: false`,
   so a direct push to `main` bypasses every check above.

**Working with an AI coding agent?** [AGENTS.md](AGENTS.md) is the operating
guide for this repository — the gates, the merge rule, the audit-invocation
traps that silently under-report results, and the recurring defect shape to
watch for. It is written for an agent but is the fastest orientation for a
human too.

## Contributing knowledge, not just code

`docs/corral/` is corralai's own developer-doc corpus — see [CORRAL.md](CORRAL.md)
for the convention. Every mission cloning this repo ingests it as advisory
memory the herd's agents can search. If you know something about this codebase
that would help an agent (or a human) working in it, open a PR against
`docs/corral/` exactly as you would for code — code review is the trust gate
for knowledge here just as it is for code; nothing you add is auto-vetted, it's
read via search until reviewed and merged.

## Cutting a release: sign the console bundle first

The console bundle's manifest is signed, and **the manifest's version is part of
the signed bytes** — so a signature covers exactly one build. Before tagging:

```sh
CORRALAI_RELEASE_KEY="$(pass show corralai/console-release-key | head -1)" \
  scripts/sign-console-bundle.sh v0.8.4
CORRALAI_CONSOLE_PUBKEY=<the public half> go run ./cmd/verify-console-signature v0.8.4
git add internal/ui/console.manifest.sig && git commit
```

Then tag — and then bump the pins, and then RE-RUN the release workflow:

```sh
git tag -a vX.Y.Z -m "<one line>" && git push origin vX.Y.Z
# main is now RED, on purpose: the pins still name the previous tag
#   ...open and merge the pin-bump PR...
gh run rerun <the release run id> --failed
```

Three gates make that order the only one that works, and the middle step
LOOKS like a break when it is not:

- `TestDocsNeverAdvertiseAnUncutActionTag` refuses a pin naming a tag that
  does not exist yet, so pins cannot be bumped before tagging.
- `TestDocsPinTheNewestCutTag` refuses docs that lag the newest tag, so from
  the moment the tag is pushed until the pin bump merges, `validate` on main
  FAILS by construction.
- The release workflow refuses to publish a release unless `validate` is green
  on the tag's commit OR on a default-branch commit that contains it — added
  2026-09-09 so a tag on red CI cannot become a release. Its first version
  demanded green on the tag's own commit, which the two gates above make
  impossible, and it blocked rc.12 until it was corrected.

So the tag push lands in the window where main is red, the release refuses,
and the rerun after the pin bump is what actually publishes. That is
deliberate; the alternative is a Releases page that can vouch for a commit
whose tests never passed.

The release workflow re-runs the console verification against the
`CORRALAI_CONSOLE_PUBKEY` secret and **fails the release** if the committed
signature does not cover the tag's version — because until that check existed,
every released brain served a console every thin client refused with
`manifest signature INVALID`, and nothing anywhere said so.

The signing seed is in `pass corralai/console-release-key` with an encrypted
backup in Hetzner's credstore. It is deliberately **not** a GitHub secret: CI
only verifies, so the private key never needs to reach a runner.
