<?php
use PHPUnit\Framework\TestCase;
use PHPUnit\Framework\Attributes\Group;

// Every test here is in the slow group: under --exclude-group slow the
// file runs nothing, and PHPUnit does not list it.
final class DTest extends TestCase
{
    #[Group('slow')]
    public function testOnly(): void { $this->assertTrue(true); }
}
