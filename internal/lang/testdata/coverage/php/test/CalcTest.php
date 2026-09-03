<?php
require_once __DIR__ . '/../lib/Calc.php';
require_once __DIR__ . '/../lib/Dead.php';

$c = new Calc();
if ($c->add(1, 2) !== 3) {
    fwrite(STDERR, "add failed\n");
    exit(1);
}
echo "ok\n";
