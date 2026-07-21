# SPDX-License-Identifier: Elastic-2.0
# Password strength validator.
#
# valid?(p) is true iff p is at least 12 characters long AND contains an
# uppercase letter, a lowercase letter, a digit, and a symbol.
def valid(p)
  return false if p.length < 12
  up = lo = di = sy = false
  p.each_char do |c|
    if c =~ /[[:upper:]]/
      up = true
    elsif c =~ /[[:lower:]]/
      lo = true
    elsif c =~ /[[:digit:]]/
      di = true
    elsif c =~ /[^[:alnum:]]/
      sy = true
    end
  end
  up && lo && di && sy
end
