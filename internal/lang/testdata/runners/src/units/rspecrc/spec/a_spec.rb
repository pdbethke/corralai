RSpec.describe "A" do
  it("one") { expect(1 + 1).to eq(2) }
  it("two", :slow) { expect(2 + 2).to eq(4) }
end
