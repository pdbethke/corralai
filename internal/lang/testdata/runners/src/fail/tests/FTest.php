<?php
// Failing fixture for the runner table (scripts/capture-runner-fixtures.sh).
use PHPUnit\Framework\TestCase;
final class FTest extends TestCase {
  public function testAdds(): void { $this->assertSame(3, 1 + 1); }
  public function testOk(): void { $this->assertTrue(true); }
  /** @dataProvider p */ #[\PHPUnit\Framework\Attributes\DataProvider('p')]
  public function testData(int $a): void { $this->assertSame(1, $a); }
  public static function p(): array { return ['one' => [1], 'two' => [2]]; }
  public function testBoom(): void { throw new \RuntimeException('boom'); }
}
