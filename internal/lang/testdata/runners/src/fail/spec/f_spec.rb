# Failing fixture for the runner table (scripts/capture-runner-fixtures.sh).
RSpec.describe 'Calc' do
  it('adds (a+b)') { expect(1 + 1).to eq(3) }
  it('ok') { }
  describe('neg') { it('adds') { raise 'boom' } }
end
module MyApp; class Calc; end; end
RSpec.describe MyApp::Calc do
  it('top level') { expect(1).to eq(2) }
end
