<?php

namespace Acme\Billing;

interface Priceable
{
    public function price(): float;

    public function currency(): string;
}

trait Loggable
{
    public function log(string $msg): void
    {
        echo $msg;
    }
}

class Invoice implements Priceable
{
    use Loggable;

    private float $total;

    public function __construct(float $total)
    {
        $this->total = $total;
    }

    public function price(): float
    {
        if ($this->total < 0) {
            return 0.0;
        }
        return $this->total;
    }

    public function currency(): string
    {
        return "USD";
    }

    public function describe(string $kind): string
    {
        $label = match ($kind) {
            'small' => 'Small invoice',
            'large' => 'Large invoice',
            default => 'Invoice',
        };
        return $label;
    }

    public function status(int $code): string
    {
        switch ($code) {
            case 1:
                return 'pending';
            case 2:
            case 3:
                return 'paid';
            default:
                return 'unknown';
        }
    }
}

function formatTotal(float $amount): string
{
    return number_format($amount, 2);
}
