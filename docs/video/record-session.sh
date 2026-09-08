#!/bin/bash
# The review video, part 2: the ruling, the chain, the query, the push — the same ledger, two verbs.
export PATH="${CORRAL_REC_VENV:-/tmp/corral-rc/venv}/bin:$HOME/go/bin:$PATH"
export motherduck_token="$(pass show corralai/motherduck-token | head -1)"
cd ${FLASK:-/tmp/corral-rc/flask}
run() { printf '\n\033[1;32m$\033[0m %s\n' "$*"; "$@"; }
sleep 1
run corral review adjudicate .corral/ledger "$1" --confirm --by pdbethke --reason "$2"
sleep 1
run corral ledger verify .corral/ledger
sleep 1
run corral brief --repo . --scope src/flask --max-items 6
sleep 1
printf "\n\033[1;32m$\033[0m %s\n" "duckdb -c \"SELECT kind, count(*) AS entries FROM read_json_auto('.corral/ledger/scans/*.json.gz') GROUP BY 1\""; python3 -c "import duckdb; print(duckdb.sql(\"SELECT coalesce(kind,'scan') AS kind, count(*) AS entries FROM read_json_auto('.corral/ledger/scans/*.json.gz') GROUP BY 1 ORDER BY 2 DESC\"))"
sleep 2
