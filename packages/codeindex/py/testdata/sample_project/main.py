"""Main fixture module — covers every node + edge kind atlas extracts.

The test harness asserts the following symbols exist:

* function ``helper``
* function ``compute``  (calls ``helper``)
* class ``BaseEntity``
* class ``MyClass``     (inherits ``BaseEntity``, decorated with ``register``)
* method ``MyClass.run`` (instance method, calls ``helper``)
* method ``MyClass.classmethod_example`` (classmethod)
* method ``MyClass.staticmethod_example`` (staticmethod)
* const ``API_VERSION``

And these edges:

* call          ``compute`` -> ``helper``
* call          ``MyClass.run`` -> ``helper``
* inheritance   ``MyClass`` -> ``BaseEntity``
* decorator     ``MyClass`` -> ``register``
* decorator     ``compute`` -> ``cached``
* import        module -> ``os``
* import        module -> ``collections.OrderedDict``
* import        module -> ``.sibling``
"""

import os  # noqa: F401 — import is the point of the fixture
from collections import OrderedDict  # noqa: F401
from . import sibling

API_VERSION = "v1"


def register(cls):
    """Identity-decorator used to verify decorator-edge extraction on classes."""
    return cls


def cached(fn):
    """Identity-decorator used to verify decorator-edge extraction on functions."""
    return fn


def helper(x: int) -> int:
    """Module-level helper called by ``compute`` and ``MyClass.run``."""
    return x + 1


@cached
def compute(seed: int = 0) -> int:
    """Calls ``helper`` so the scanner emits a function-to-function call edge."""
    return helper(seed) + helper(seed + 1)


class BaseEntity:
    """Plain base class — target of an inheritance edge from ``MyClass``."""

    def base_method(self) -> str:
        return "base"


@register
class MyClass(BaseEntity):
    """Decorated subclass — exercises class decorator + inheritance edges."""

    def __init__(self, name: str) -> None:
        self.name = name

    def run(self) -> int:
        """Instance method — calls ``helper`` + ``sibling.sibling_helper``."""
        return helper(1) + sibling.sibling_helper(2)

    @classmethod
    def classmethod_example(cls) -> str:
        return cls.__name__

    @staticmethod
    def staticmethod_example() -> int:
        return 42
