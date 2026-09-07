# corral/ledger — the record

This branch is corral's own ledger: one signed, hash-linked entry per audit
event on this repository — reviews, refutations, adjudications, scans — as
gzipped JSON under `scans/`. It is the paper trail, not a copy of it: the
files are the record, and any DuckDB reads them in place.

Verify the chain from a clean checkout:

    go install github.com/pdbethke/corralai/cmd/corral@v1.0.0-rc.8
    corral ledger verify .

The chain begins where the paper trail began (2026-09-06, the review loop's
first run on this repository); the audits that ran before a ledger existed
wrote to nothing and are not here. Entries before `corral-ledger-3` were hashed
in the sparse form; the review that found that defect is itself recorded
under it, and the fix is the next thing in the chain. `corral ledger verify`
says which rule each entry was checked under.

Nothing here is ever edited. A wrong entry is retracted by a later entry.
