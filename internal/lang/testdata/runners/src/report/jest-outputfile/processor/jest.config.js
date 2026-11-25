// Report fixture (scripts/capture-runner-fixtures.sh): a testResultsProcessor of the project's that writes theirs.json on every run, with or without the failure hook; the hook must not add a write.
module.exports = { testResultsProcessor: "<rootDir>/proc.js" };
