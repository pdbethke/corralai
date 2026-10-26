<?php
// REQUIRED by the suite and never called. pcov reports an executed line for
// the file's implicit include marker (one past the last line), so a naive
// any-positive-line rule calls this file covered. Method-body coverage does
// not — this file is the negative control. Its top-level statement DOES run
// on require, so the report names the file loaded: import-only, never "no
// unit executes this file".
const DEAD_FACTOR = 2;

class Dead
{
    public function neverCalled(int $x): int
    {
        return $x * DEAD_FACTOR;
    }
}
