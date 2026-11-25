<?php
// Failure-hook edge fixture (scripts/capture-runner-fixtures.sh phpunit-hook-edge.txt).
// A setUpBeforeClass that throws: PHPUnit prints `N) SetupTest` and its JUnit record omits it.
use PHPUnit\Framework\TestCase;
final class SetupTest extends TestCase {
  public static function setUpBeforeClass(): void { throw new \RuntimeException('setup boom'); }
  public function testA(): void { $this->assertTrue(true); }
  public function testB(): void { $this->assertTrue(true); }
}
