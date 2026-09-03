<?php
// Never required: must be ABSENT from the map, not reported false.
class Unloaded
{
    public function nope(): int
    {
        return 1;
    }
}
