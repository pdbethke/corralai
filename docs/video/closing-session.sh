#!/bin/bash
# The closing frame, shared by both videos: a stranger's check of corral's own record.
export PATH="${CORRAL_REC_VENV:-/tmp/corral-rc/venv}/bin:$HOME/go/bin:$PATH"
export motherduck_token="$(pass show corralai/motherduck-token | head -1)"
cd "${TMPDIR:-/tmp}" && rm -rf ledger-clone
run() { printf '\n\033[1;32m$\033[0m %s\n' "$*"; "$@"; }
sleep 1
run git clone -q --branch corral/ledger --depth 1 https://github.com/pdbethke/corralai.git ledger-clone
cd ledger-clone
run corral ledger verify . 
sleep 1
printf '\n\033[1;32m$\033[0m %s\n' "duckdb -c \"SELECT kind, count(*) AS entries FROM read_json_auto('scans/*.json.gz') GROUP BY 1 ORDER BY 2 DESC\""
python3 -c "
import duckdb
print(duckdb.sql(\"SELECT coalesce(kind,'scan') AS kind, count(*) AS entries FROM read_json_auto('scans/*.json.gz') GROUP BY 1 ORDER BY 2 DESC\"))"
sleep 1
run corral ledger push . md:corral_public
sleep 1
run corral models rank --db md:corral_public --seat reviewer --min-runs 3
sleep 2
