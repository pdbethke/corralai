# Fixture for the runner table's rspec row (scripts/capture-runner-fixtures.sh).
RSpec.describe 'Calc' do
  it('adds (a+b)') { expect(1 + 1).to eq 2 }
  it('adds') {}
  it('adds x') {}
  describe('neg') { it('adds') {} }
end
RSpec.describe 'Calc adds' do
  it('x') {}
end
RSpec.describe 'top' do
  it('level') {}
end
