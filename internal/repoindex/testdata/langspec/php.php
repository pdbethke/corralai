<?php

function total(array $items): int
{
    $sum = 0;
    foreach ($items as $it) {
        if ($it < 0 && $sum > 0) {
            continue;
        } elseif ($it === 0 || $it === null) {
            $sum += 0;
        }
        $sum += $it;
    }
    while ($sum > 1000) {
        $sum--;
    }
    do {
        $sum++;
    } while ($sum < 0);
    switch ($sum) {
        case 0:
            return 0;
        default:
            break;
    }
    try {
        $sum = intval($sum);
    } catch (Exception $e) {
        $sum = 0;
    }
    $label = match ($sum) {
        1 => 'one',
        default => 'many',
    };
    return $sum > 0 ? $sum : 0;
}

class Cart
{
    private array $items = [];

    public function size(): int
    {
        return count($this->items) > 0 ? count($this->items) : 0;
    }

    private function clear(): void
    {
        $this->items = [];
    }
}
