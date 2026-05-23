"""Atlas Python helper — copy into your project to enable decorator-style
``@atlas.feature("id")`` annotations.

Atlas reads these decorators statically (via the AST scanner in
``packages/codeindex/py/scanner.py``); at runtime they are no-ops. There
is no pip install — drop this file into your project (e.g. as
``atlas.py`` at a package root) and import the names you want.

Mirror set of the comment-form kinds in
``packages/codeindex/annotations/parser.go`` that take a single string id
payload. Free-form kinds (``owner``, ``deprecated``, ``since``) are
intentionally omitted from the decorator surface because a string
argument is the wrong shape for "alice@team.com" or a free-text
deprecation reason; use the comment form for those.

Usage::

    from atlas import feature, contract, aggregate

    @feature("billing.subscribe")
    def subscribe(user_id, plan):
        ...

    @aggregate("billing.subscription")
    class BillingSubscription:
        ...

The annotation propagates from the class to every method when applied at
the class level — same semantics as the comment form.
"""

from __future__ import annotations

from typing import Any, Callable, TypeVar

T = TypeVar("T", bound=Callable[..., Any] | type)


def _identity(_id: str) -> Callable[[T], T]:
    """Return a no-op decorator that ignores its argument and returns the
    wrapped object unchanged. The id is read statically by atlas; at
    runtime it has no effect."""

    def wrap(obj: T) -> T:
        return obj

    return wrap


# One alias per id-shaped annotation kind. Naming mirrors the
# ``@atlas:<kind>`` grammar segments — `feature`, `contract`, `bc`,
# `aggregate`, `aggregate_service` (Python identifier; the wire form is
# the kebab-case ``aggregate-service``), `saga`, `consumer`,
# ``event_emit``, ``outbox_publish``.
feature = _identity
contract = _identity
bc = _identity
aggregate = _identity
aggregate_service = _identity
saga = _identity
consumer = _identity
event_emit = _identity
outbox_publish = _identity
