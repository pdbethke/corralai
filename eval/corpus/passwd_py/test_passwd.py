# SPDX-License-Identifier: Elastic-2.0
"""The 'gappy' suite: it only ever feeds a VALID password (and one too-short
one), so it never exercises the four character-class rules — a mutant that
drops, say, the digit requirement passes it undetected."""
import passwd


def test_valid_length():
    assert passwd.valid("Abcdefgh1!xy")


def test_too_short():
    assert not passwd.valid("Ab1!xyz")
