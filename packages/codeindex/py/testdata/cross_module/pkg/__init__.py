"""cross_module fixture — exercises issue #61 cross-module resolver rules.

This package's ``__init__`` re-exports ``echo`` from ``.termui`` so callers
inside ``pkg.core`` that reference the bare ``echo`` should resolve to
``pkg.termui.echo`` via rule (4) (re-export from caller's package init).
"""

from .termui import echo  # noqa: F401 — re-export drives rule (4)
