// Report fixture (scripts/capture-runner-fixtures.sh): a jest config that asks for a report file of the project's own, theirs.json; the failure hook must never write it.
module.exports = { json: true, outputFile: "theirs.json" };
