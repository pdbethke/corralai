// Report fixture (scripts/capture-runner-fixtures.sh): a testResultsProcessor of the project's that writes theirs.json on every run, with or without the failure hook; the hook must not add a write.
const fs = require("fs");
module.exports = (r) => { fs.writeFileSync(__dirname + "/theirs.json", JSON.stringify(r.numFailedTests)); return r; };
