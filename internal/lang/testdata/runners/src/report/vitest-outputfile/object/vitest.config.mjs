// Report fixture (scripts/capture-runner-fixtures.sh): a project config that names its own report file, in one of the shapes vitest takes.
export default { test: { outputFile: { junit: 'theirs.xml', json: 'theirs.json' } } };
