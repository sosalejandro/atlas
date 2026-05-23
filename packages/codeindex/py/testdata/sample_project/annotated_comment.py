"""Comment-style annotation fixture.

The scanner must surface the ``# @atlas:feature ...`` comment above
``ingest_rows`` as an annotation attached to that function, anchored at
the function's source line so the store-side ``LookupSymbolAtOrAfterLine``
resolves to the function symbol.
"""

from __future__ import annotations


# @atlas:feature ingest-csv-imports
def ingest_rows(path: str) -> int:
    """Pure-fixture body — read a path, return a row count."""
    return _read_count(path)


# @atlas:contract ingest-csv-imports.parse-row
def parse_row(line: str) -> dict:
    """Pure-fixture body — split a CSV line into a dict."""
    return {"raw": line}


def _read_count(path: str) -> int:
    """Helper without an annotation — must NOT be surfaced as feature-linked."""
    return len(path)
