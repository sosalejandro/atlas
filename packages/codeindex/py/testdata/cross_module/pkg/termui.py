"""termui — provides `echo` (re-exported via __init__) and `style`.

Both functions call a private `_format` helper so the depth-3 trace
walk through ``pkg.core.command`` lands on at least one symbol two
hops away from the entry point.
"""


def _format(value: str) -> str:
    """Private helper called by both `echo` and `style`."""
    return value.strip()


def echo(message: str) -> str:
    """Re-exported by pkg/__init__.py."""
    return _format(message)


def style(message: str) -> str:
    """Imported directly by pkg.core.command."""
    return _format(message)
