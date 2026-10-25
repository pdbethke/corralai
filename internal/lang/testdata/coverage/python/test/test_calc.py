# SPDX-License-Identifier: Elastic-2.0
import lib.empty  # noqa: F401
from lib.calc import add


def test_add():
    assert add(1, 2) == 3
