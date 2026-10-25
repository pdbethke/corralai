<?php
use PHPUnit\Framework\TestCase;

final class BTest extends TestCase
{
    public function testOne(): void { $this->assertSame(3, 1 + 2); }
}
