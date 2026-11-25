# Failing fixture for the rspec row's failure hook (scripts/capture-runner-fixtures.sh): the shapes where rspec's JSON record and its printed `Failed examples:` list could disagree.
RSpec.describe 'BeforeAll' do
  before(:context) { raise 'setup' }
  it('one') { }
end
RSpec.describe 'AfterAll' do
  after(:context) { raise 'teardown' }
  it('two') { }
end
RSpec.describe 'Pending' do
  it('fixed') { pending('not yet'); expect(1).to eq(1) }
  it('still') { pending('not yet'); expect(1).to eq(2) }
end
RSpec.describe 'Agg' do
  it('many', :aggregate_failures) { expect(1).to eq(2); expect(2).to eq(3) }
end
RSpec.shared_examples 'shared' do
  it('in shared') { expect(1).to eq(2) }
end
RSpec.describe 'Shared' do
  it_behaves_like 'shared'
end
