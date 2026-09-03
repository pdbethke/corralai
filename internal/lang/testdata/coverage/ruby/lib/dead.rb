# REQUIRED by the suite but never called. Line coverage reports this file as
# executed (the class/def lines run on require); method coverage does not.
class Dead
  def never_called(x)
    x * 2
  end
end
