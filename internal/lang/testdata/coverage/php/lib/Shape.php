<?php
// SPDX-License-Identifier: Elastic-2.0
// REQUIRED by the suite and DECLARATION-ONLY: an interface has no body that
// could run. pcov still reports one hit for it, on the file's implicit
// include marker one line past its end, so this file is the control for
// "nothing executable must be absent, never measured-and-run".
interface Shape
{
    public function area(): float;
}
