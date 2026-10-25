<?php
// Fixture for the runner table's phpunit unit form (scripts/capture-runner-fixtures.sh): two tests, one slow.
use PHPUnit\Framework\TestCase;
use PHPUnit\Framework\Attributes\Group;

final class ATest extends TestCase
{
    public function testOne(): void { $this->assertSame(2, 1 + 1); }
    #[Group('slow')]
    public function testTwo(): void { $this->assertSame(4, 2 + 2); }
}
