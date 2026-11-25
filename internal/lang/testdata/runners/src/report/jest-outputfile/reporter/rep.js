// Report fixture (scripts/capture-runner-fixtures.sh): a jest config that asks for a report file of the project's own, theirs.json; the failure hook must never write it.
const fs = require("fs");
module.exports = class { constructor(g, o) { this.o = o; } onRunComplete(c, r) { fs.writeFileSync(this.o.outputFile, JSON.stringify(r.numFailedTests)); } };
