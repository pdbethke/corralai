# SPDX-License-Identifier: Elastic-2.0
"""Password strength validator."""


def valid(p: str) -> bool:
    """Return True iff p is a valid password: length >= 12 AND it contains an
    uppercase letter, a lowercase letter, a digit, and a symbol."""
    if len(p) < 12:
        return False
    up = lo = di = sy = False
    for c in p:
        if c.isupper():
            up = True
        elif c.islower():
            lo = True
        elif c.isdigit():
            di = True
        elif not c.isalnum():
            sy = True
    return up and lo and di and sy
