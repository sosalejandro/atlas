"""Decorator-style annotation fixture.

Two surfaces under test:

1. ``@atlas.feature("ship-orders")`` on a function — must surface as an
   annotation attached to ``ship_one``.
2. ``@atlas.feature("ship-orders")`` on a class — must surface as an
   annotation attached to the class AND propagate to every method body
   defined inside the class (the AC #6 class-level propagation).

The helper module shipped at ``assets/python/atlas.py`` provides the
no-op ``feature`` decorator; in this fixture we don't import it because
the scanner reads the decorator name statically.
"""

from __future__ import annotations

import atlas  # noqa: F401 — runtime no-op; scanner reads statically


@atlas.feature("ship-orders")
def ship_one(order_id: str) -> str:
    """Ship a single order — function-level decorator."""
    return f"shipped:{order_id}"


@atlas.feature("ship-orders.batch")
class BatchShipper:
    """Class-level decorator — propagates to every method below.

    The store-side LookupSymbolAtOrAfterLine resolves each propagation
    record to the method's symbol, NOT to the class. That's what makes
    ``atlas trace ship-orders.batch`` return the method bodies rather
    than just the class declaration.
    """

    def __init__(self) -> None:
        self._pending: list[str] = []

    def enqueue(self, order_id: str) -> None:
        """Method that should inherit the class-level feature link."""
        self._pending.append(order_id)

    def flush(self) -> int:
        """Another inheriting method — exercises propagation across siblings."""
        count = len(self._pending)
        self._pending.clear()
        return count
