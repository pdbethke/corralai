# Fixture for the Ruby test lister (scripts/capture-runner-fixtures.sh): how
# rspec joins descriptions. A group's full description drops the space before
# a "#", "." or "::" child only when its parent's last argument is a Module;
# an example's always takes a space.
class Calc; end
module MyApp; class Calc; end; end
RSpec.describe 'Str' do
  describe '#add' do
    it('sums') {}
  end
end
RSpec.describe Calc do
  it('#mul works') {}
  it('.build') {}
  describe '#add' do
    it('x') {}
  end
end
RSpec.describe Calc, '#sub' do
  it('subs') {}
end
RSpec.describe Calc, 'x' do
  describe '#y' do
    it('z') {}
  end
end
RSpec.describe :sym do
  describe '#q' do
    it('r') {}
  end
end
RSpec.describe MyApp::Calc do
  describe '.make' do
    it('b') {}
  end
end
RSpec.describe 'A', 'B' do
  it('c') {}
end
