<?php
// Fixture for the runner table's phpunit row (scripts/capture-runner-fixtures.sh).
use PHPUnit\Framework\TestCase;
use PHPUnit\Framework\Attributes\DataProvider;

final class CalcTest extends TestCase
{
    public function testAdds(): void { $this->assertSame(2, 1 + 1); }
    public function testAddsMore(): void { $this->assertTrue(true); }
    #[DataProvider('sets')]
    public function testWith(int $a): void { $this->assertIsInt($a); }
    public static function sets(): array { return ['one' => [1], 'two' => [2]]; }
}
