"""core — exercises every cross-module resolver rule against sibling and
re-exported names.

Edges this module should produce after rule application:

* ``pkg.core.command`` calls ``echo``      → resolves via re-export (rule 4)
                                              to ``pkg.termui.echo``
* ``pkg.core.command`` calls ``style``      → resolves via caller's import
                                              (rule 3) to ``pkg.termui.style``
* ``pkg.core.command`` calls ``sibling_fn`` → resolves via sibling lookup
                                              (rule 5) to ``pkg.helpers.sibling_fn``
* ``pkg.core.command`` calls ``deep_helper`` → multi-hop through the sub
                                              package, resolves via rule 5 to
                                              ``pkg.sub.deep.deep_helper``
"""

from .termui import style


def command() -> str:
    """Walked by the trace integration test — depth 3 lands on `_format`."""
    a = echo("hello")  # resolved via re-export from pkg/__init__.py
    b = style("world")  # resolved via this module's `from .termui import style`
    c = sibling_fn(1)  # resolved via sibling lookup against pkg/helpers.py
    d = deep_helper(2)  # resolved via sibling lookup (top-level in sub.deep)
    return f"{a}-{b}-{c}-{d}"
