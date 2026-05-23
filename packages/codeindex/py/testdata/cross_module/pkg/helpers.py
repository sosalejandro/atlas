"""helpers — sibling module never explicitly imported by pkg.core.

Exists to validate rule (5): sibling-module top-level lookup. The trace
test asserts that ``sibling_fn`` resolves to ``pkg.helpers.sibling_fn``
even with no import edge connecting the two modules.
"""


def sibling_fn(n: int) -> int:
    """Trivial helper resolved only via sibling-module lookup."""
    return n * 2
