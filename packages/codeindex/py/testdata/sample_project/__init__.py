"""sample_project — fixture for atlas Python AST scanner integration tests.

Exercises every node + edge shape the scanner is contracted to emit:

* module-level function + function-to-function call edge
* class with inheritance, decorator, instance method
* method calling a module-level helper (cross-symbol call edge)
* absolute import (``import os``)
* from-import (``from collections import OrderedDict``)
* relative import (``from . import sibling``)

The sibling-module file and a deliberately-broken file live next to this
one; see scanner_test.go for the assertions.
"""
