# The PARENT loads the library and never calls it, then runs the real suite in
# a CHILD process. This is the shape of a Rakefile that shells out, and of any
# runner that forks workers.
#
# It is the regression fixture for a false FINDING, not merely lost data: with
# a single shared report path, every process truncates it and the parent exits
# LAST, so the parent's "loaded but nothing called" verdict for calc.rb
# overwrote the child's "executed" one — and corral printed a covered file
# under "measured and NEVER executed by the suite".
require_relative 'lib/calc'
require_relative 'lib/dead'

ok = system('ruby', '-Ilib', File.join(__dir__, 'test', 'calc_test.rb'))
exit(ok ? 0 : 1)
