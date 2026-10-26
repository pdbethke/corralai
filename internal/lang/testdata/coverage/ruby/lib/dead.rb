# REQUIRED by the suite but never called. Line coverage reports this file as
# executed (the class/def lines run on require); method coverage does not.
# The class body and its constant DO run on require, so the report names the
# file loaded: import-only, never "no unit executes this file".
class Dead
  FACTOR = 2

  def never_called(x)
    x * FACTOR
  end
end
