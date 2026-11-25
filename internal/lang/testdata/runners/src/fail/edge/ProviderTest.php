<?php
// Failure-hook edge fixture (scripts/capture-runner-fixtures.sh phpunit-hook-edge.txt).
// A data provider that throws: PHPUnit prints a "PHPUnit error" for the method and its JUnit record omits it.
use PHPUnit\Framework\TestCase;
final class ProviderTest extends TestCase {
  #[\PHPUnit\Framework\Attributes\DataProvider('p')]
  public function testP(int $a): void { $this->assertSame(1, $a); }
  public static function p(): array { throw new \RuntimeException('provider boom'); }
  public function testFine(): void { $this->assertTrue(true); }
}
