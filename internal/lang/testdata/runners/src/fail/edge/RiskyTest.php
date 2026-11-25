<?php
// Failure-hook edge fixture (scripts/capture-runner-fixtures.sh phpunit-hook-edge.txt).
// A test with no assertion: PHPUnit reports it risky (not failing) and its JUnit record lists it as a plain testcase.
use PHPUnit\Framework\TestCase;
final class RiskyTest extends TestCase {
  public function testNothing(): void { }
  public function testFine(): void { $this->assertTrue(true); }
}
