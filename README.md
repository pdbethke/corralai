# corral/ledger — the record

This branch is corral's own ledger: one signed, hash-linked entry per audit
event — scans, reviews, refutations, adjudications — as gzipped JSON under
`scans/`. It is the paper trail, not a copy of it: the files are the record,
and any DuckDB reads them in place.

Verify the chain from a clean checkout:

    go install github.com/pdbethke/corralai/cmd/corral@v1.0.0-rc.31
    corral verify --ledger . --pub $(cat LEDGER_PUBKEY)

## The chain begins at a checkpoint (2026-11-09)

The first entry is a checkpoint. It names the head it replaced (entry
`e4f4aa7b6d96`, 168 entries recorded from 2026-09-06 to 2026-11-09) and the
verifier says so. The record was restarted so that every entry on it is in
the complete format, which names each audited file's language, its runner,
the substrate and the release that graded it, and was graded by the engine
that runs today. Most of the earlier entries could not say what graded them,
and every one of them predates kill confirmation (rc.31), so none of them
can serve as proof of what corral does now.

The earlier chain is kept, not deleted. The tag `ledger-archive/2026-11-09`
is this branch as it stood before the checkpoint, and it still verifies:

    git clone --branch ledger-archive/2026-11-09 https://github.com/pdbethke/corralai archive
    corral verify --ledger archive --pub $(cat archive/LEDGER_PUBKEY)

It holds the review rounds against corral itself, their adjudications, and
the scans of flask, Warp, urfave/cli, starlette, pylint and REA.

A wrong entry is retracted by a later entry. Entries are never edited.
