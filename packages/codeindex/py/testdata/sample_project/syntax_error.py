# Intentionally invalid Python — verifies scanner.py reports the file
# without crashing the scan. The bare `def` with no name + no body is an
# unambiguous SyntaxError that ast.parse rejects.
def
