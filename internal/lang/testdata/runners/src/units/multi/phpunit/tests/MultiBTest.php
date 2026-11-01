<?php
// Fixture for the runner table's multi-unit commands (scripts/capture-runner-fixtures.sh): a holds one test, b two, c four, so a run's count names the set it ran.
use PHPUnit\Framework\TestCase;

final class MultiBTest extends TestCase
{
    public function testBOne(): void { $this->assertSame(1, 1); }
    public function testBTwo(): void { $this->assertSame(1, 1); }
}
