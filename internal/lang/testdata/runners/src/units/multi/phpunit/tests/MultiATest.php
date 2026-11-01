<?php
// Fixture for the runner table's multi-unit commands (scripts/capture-runner-fixtures.sh): a holds one test, b two, c four, so a run's count names the set it ran.
use PHPUnit\Framework\TestCase;

final class MultiATest extends TestCase
{
    public function testAOne(): void { $this->assertSame(1, 1); }
}
