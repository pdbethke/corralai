<?php
// Exercised by the suite: add is called, sub is not.
class Calc
{
    public function add(int $a, int $b): int
    {
        return $a + $b;
    }

    public function sub(int $a, int $b): int
    {
        return $a - $b;
    }
}
