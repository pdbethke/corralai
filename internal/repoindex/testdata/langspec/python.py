import functools


def total(items, strict=False):
    s = 0
    for it in items:
        if it < 0 and strict:
            raise ValueError(it)
        elif it == 0 or it is None:
            continue
        s += it
    while s > 1000:
        s -= 1
    try:
        s = int(s)
    except TypeError:
        s = 0
    match s:
        case 0:
            return None
    return s if s > 0 else 0


class Cart:
    def __init__(self):
        self.items = []

    @functools.lru_cache
    def size(self):
        return len(self.items) if self.items else 0


@functools.cache
def decorated(x):
    if x:
        return 1
    return 2
