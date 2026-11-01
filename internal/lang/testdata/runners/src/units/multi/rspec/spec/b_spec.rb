# Fixture for the runner table's multi-unit commands (scripts/capture-runner-fixtures.sh): a holds one test, b two, c four, so a run's count names the set it ran.
RSpec.describe "Multi B" do
  it("b one") { expect(1).to eq(1) }
  it("b two") { expect(1).to eq(1) }
end
