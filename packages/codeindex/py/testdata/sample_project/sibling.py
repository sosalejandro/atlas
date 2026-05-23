"""Sibling module — target of a relative import in main.py."""


def sibling_helper(value: int) -> int:
    """Trivial helper imported by main.py via ``from . import sibling``."""
    return value * 2
