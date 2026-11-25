<?php
// Namespaced failing fixture (scripts/capture-runner-fixtures.sh): PHPUnit's
// JUnit record writes this class as class="App\Tests\NTest" and
// classname="App.Tests.NTest"; it prints and filters by the first.
namespace App\Tests;
use PHPUnit\Framework\TestCase;
final class NTest extends TestCase {
  public function testNamespaced(): void { $this->assertSame(3, 1 + 1); }
}
