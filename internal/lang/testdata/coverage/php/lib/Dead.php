<?php
// REQUIRED by the suite and never called. pcov reports an executed line for
// the file's implicit include marker (one past the last line), so a naive
// any-positive-line rule calls this file covered. Method-body coverage does
// not — this file is the negative control.
class Dead
{
    public function neverCalled(int $x): int
    {
        return $x * 2;
    }
}
