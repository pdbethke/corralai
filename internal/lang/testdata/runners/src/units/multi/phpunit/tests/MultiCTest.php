<?php
// Fixture for the runner table's multi-unit commands (scripts/capture-runner-fixtures.sh): a holds one test, b two, c four, so a run's count names the set it ran.
use PHPUnit\Framework\TestCase;

final class MultiCTest extends TestCase
{
    public function testCOne(): void { $this->assertSame(1, 1); }
    public function testCTwo(): void { $this->assertSame(1, 1); }
    public function testCThree(): void { $this->assertSame(1, 1); }
    public function testCFour(): void { $this->assertSame(1, 1); }
}
