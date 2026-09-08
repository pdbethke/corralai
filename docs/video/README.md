# The two videos

Two recordings of corral's extensions on a repository it did not write — flask
at `36e4a824` — and one shared closing frame on corral's own record. Each is a
real run; the pipeline applies pacing and captions and nothing else.

| video | what it is | length |
|---|---|---|
| `corral-certify.mp4` | `certify --repo . --top 1` on flask: goals derived, forty faults planted in `app.py`, flask's own suite run against each in a jail, 25 killed / 15 survived, a test written for every survivor and proven alone against its fault, 14 of 15 proven, the signed verdict, the ledger entry, the chain verified | 61 s |
| `corral-review.mp4` | `review --scope src/flask/sessions.py` with Claude Code reviewing and Codex verifying: five claims, two declared reproduced whose scripts did not demonstrate them (demoted on the record), five standing as read; a person checks the top claim by hand (`session \|= {"a": 1}` on an app with no secret key mutates the null session silently) and rules; `ledger verify`, `brief`, a DuckDB query over the branch | 71 s |

Both end on the same frame: a fresh clone of `corral/ledger`, `corral ledger
verify .`, one `SELECT` over the entries, `corral ledger push . md:corral_public`,
and `models rank --db md:corral_public --seat reviewer`.

## What is real and what is applied

- `*.cast` are the asciinema recordings of the runs, byte for byte, with the
  original timings. Nothing in them was edited.
- The certify segment plays at 1× with every wait on the jail or a model
  capped at 3.5 s (`--idle-time-limit`). The closing segment: 1×, waits capped
  at 6 s.
- The review's output arrives as one burst after the seats' five-minute
  silence. The burst is re-timed one line every 0.11 s (`retime_burst`) so it
  can be read; same bytes, same order. The wait before it is capped at 6 s.
- The last frame of each segment is held a few seconds so the output can be
  read. The first caption of each video says what was applied.
- The cartoon opens both.

## Rebuild

    pip install asciinema duckdb            # asciinema to record, duckdb for the SELECT
    cargo install agg                       # or set AGG=/path/to/agg
    python3 make-videos.py                  # casts in, corral-certify.mp4 + corral-review.mp4 out

To record fresh casts (a real run each; keys in `pass`, flask at `$FLASK`):

    asciinema rec --cols 120 --rows 34 -i 3 -c ./certify-session.sh certify.cast
    asciinema rec --cols 120 --rows 34 -i 3 -c ./review-session.sh  review.cast
    asciinema rec --cols 120 --rows 34 -i 3 -c "./record-session.sh <review hash>#R1 '<reason>'" record.cast
    asciinema rec --cols 120 --rows 34 -i 3 -c ./closing-session.sh closing.cast

`opener.mp4` is the cartoon, 3.5 s, built by `make-videos.py` from
`docs/design/corral-cartoon.jpeg` when present.
