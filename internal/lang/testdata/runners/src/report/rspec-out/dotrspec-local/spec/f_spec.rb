# Report fixture (scripts/capture-runner-fixtures.sh): a failing spec run under an rspec configuration that asks for a report file of the project's own (theirs.json); the failure hook must never write it.
RSpec.describe 'Calc' do
  it('adds (a+b)') { expect(1 + 1).to eq(3) }
  it('ok') { }
  describe('neg') { it('adds') { raise 'boom' } }
end
module MyApp; class Calc; end; end
RSpec.describe MyApp::Calc do
  it('top level') { expect(1).to eq(2) }
end
