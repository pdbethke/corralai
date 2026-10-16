# Failing fixture for the runner table (scripts/capture-runner-fixtures.sh).
require 'minitest/autorun'
class CalcTest < Minitest::Test
  def test_adds; assert_equal 3, 1 + 1; end
  def test_ok; end
  def test_boom; raise 'boom'; end
end
module MyApp; class NegTest < Minitest::Test; def test_x; assert false; end; end; end
