require 'minitest/autorun'
require_relative '../lib/calc'
require_relative '../lib/dead'

class CalcTest < Minitest::Test
  def test_add
    assert_equal 3, Calc.new.add(1, 2)
  end
end
