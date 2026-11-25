# Report fixture (scripts/capture-runner-fixtures.sh): project code that writes a report file of its own, theirs.json, on every run, with or without the failure hook; the hook must not add a write.
RSpec.configure { |c| c.output_stream = File.open("theirs.json", "w") }
