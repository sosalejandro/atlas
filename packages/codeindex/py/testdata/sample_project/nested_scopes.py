"""Issue #18 regression fixture — nested-scope call-edge attribution.

The pre-fix ``_emit_call_edges`` used ``ast.walk(func)`` which descended
into inner ``def`` / ``class`` bodies. That double-counted every call
made by a nested closure under the enclosing function as well as under
the inner symbol — collapsing caller identity.

The scanner now bounds the walk at any new named scope. This fixture
shapes the bug surface explicitly so a regression would re-introduce a
duplicate edge the test can see.

Layout:

* ``outer()`` defines ``inside()`` which calls ``leaf_only()``.
  Pre-fix: ``outer -> leaf_only`` AND ``outer.inside -> leaf_only``.
  Post-fix: ``outer.inside -> leaf_only`` only.

* ``DoubleNested.method`` defines ``closure()`` which calls
  ``deep_helper()`` plus a comprehension ``[x for x in items
  if needs(x)]``.
  Pre-fix: ``method -> deep_helper`` AND ``method.closure -> deep_helper``.
  Post-fix: ``method.closure -> deep_helper`` only. The comprehension's
  ``needs(x)`` call MUST still attribute to ``method`` itself (Python-3
  comprehensions create a scope but no nameable symbol — they belong to
  the enclosing function for navigation purposes).
"""


def leaf_only() -> None:
    """Sentinel: only the nested closure should call this."""


def deep_helper() -> None:
    """Sentinel: only the deeply-nested closure should call this."""


def needs(_x: int) -> bool:
    """Sentinel: used inside a comprehension; attributes to the method."""
    return True


def outer() -> None:
    """Outer function with one nested closure."""

    def inside() -> None:
        leaf_only()

    inside()


class DoubleNested:
    """Class whose method houses a nested closure + a comprehension."""

    def method(self, items: list[int]) -> list[int]:
        def closure() -> None:
            deep_helper()

        closure()
        # Comprehension scope: no nameable symbol → attributes to method.
        return [x for x in items if needs(x)]
