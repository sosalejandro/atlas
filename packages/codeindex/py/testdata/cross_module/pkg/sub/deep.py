"""deep — nested module hosting deep_helper.

Sibling-lookup resolution from pkg.core spans nested packages: the
sibling-index is keyed by parent package, so deep_helper (declared
inside pkg.sub.deep, parent package pkg.sub) would NOT be a sibling
of pkg.core under a strict "same directory" rule. Instead, the
resolver walks the per-package sibling bucket for pkg first, then
falls back to pkg.sub for symbols not found one level up.

The current implementation indexes only direct siblings — so
deep_helper is found via pkg.sub's bucket only when callers live in
pkg.sub. From pkg.core (parent = pkg) we expect this call to remain
unresolved and stub out as `external:py`. The fixture preserves this
intentional limitation as a regression guard.
"""


def deep_helper(n: int) -> int:
    """Nested helper — out of reach of pkg.core's sibling lookup."""
    return n + 100
