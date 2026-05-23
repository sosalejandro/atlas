"""sub — empty package init (deliberate; sub's __init__ has no re-exports).

Forces the resolver's sibling-lookup to consider ``pkg.sub.deep`` as a
top-level-of-pkg sibling when ``pkg.core`` references ``deep_helper``.
The sub package keeps deep nesting in the fixture without colluding
via re-export.
"""
