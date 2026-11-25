<?php
// Data-set fixture for the runner table (scripts/capture-runner-fixtures.sh):
// PHPUnit prints a failing data set as `Class::method with data set "name"
// (<the data set's arguments>)` or `... #k (<arguments>)`; the arguments are
// chosen so that none equals the data set's position, and a name and an
// argument each hold parentheses and quotes.
use PHPUnit\Framework\TestCase;
final class DTest extends TestCase {
  #[\PHPUnit\Framework\Attributes\DataProvider('named')]
  public function testNamed(int $a): void { $this->assertSame(1, $a); }
  public static function named(): array { return ['first' => [7], 'second' => [1], 'third' => [9], 'x (y)' => [3]]; }
  #[\PHPUnit\Framework\Attributes\DataProvider('unnamed')]
  public function testUnnamed(int $a, string $s): void { $this->assertSame(1, $a); }
  public static function unnamed(): array { return [[5, 'x'], [1, 'y'], [9, 'z (y) "q"']]; }
}
