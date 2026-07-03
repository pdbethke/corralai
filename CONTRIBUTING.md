# Contributing to CorralAI

CorralAI is **source-available** under the [Elastic License 2.0](LICENSE):
read it, modify it, self-host it. The one thing you can't do is offer it to
third parties as a hosted or managed service — that path is available under a
commercial license (contact pdbethke@gmail.com).

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

## Contributing knowledge, not just code

`docs/corral/` is corralai's own developer-doc corpus — see [CORRAL.md](CORRAL.md)
for the convention. Every mission cloning this repo ingests it as advisory
memory the herd's agents can search. If you know something about this codebase
that would help an agent (or a human) working in it, open a PR against
`docs/corral/` exactly as you would for code — code review is the trust gate
for knowledge here just as it is for code; nothing you add is auto-vetted, it's
read via search until reviewed and merged.
