#!/usr/bin/env python3
"""Atlas Python AST Scanner.

Mirror of packages/codeindex/ts/scanner.ts but driven by Python's stdlib
`ast` module. The Atlas Go orchestrator (packages/codeindex/py/scanner.go)
embeds this file via //go:embed, writes it to a tempfile at runtime, and
shells out to python3.

Output contract (JSON, one object on stdout)::

    {
      "nodes": [{"id", "kind", "file", "line", "doc"}, ...],
      "edges": [{"from", "to", "kind"}, ...],
      "files": [{"path", "syntax_error"}, ...],
      "warnings": ["..."],
      "stats": {"files_scanned", "symbols_found", "edges_found",
                "syntax_failures"}
    }

The Go orchestrator maps node.kind onto shared.SymbolKind and emits
graph.Edge values verbatim. All file paths are repo-relative
(forward-slash) so the persisted shared.FilePosition is portable.

Usage::

    python3 scanner.py --root <project-root>
                       [--include <dir>]...
                       [--exclude <dir>]...

The CLI flags are forwarded by the Go layer. With no --include, the
scanner walks the entire project root, skipping the always-excluded
directories listed in ``DEFAULT_SKIP_DIRS``.

Constraints:
    * Pure stdlib (``ast``, ``json``, ``sys``, ``os``, ``argparse``).
    * No pip dependencies — atlas's value prop is "just works once
      python3 is on PATH".
    * Per the issue spec (out-of-scope), the scanner does NOT parse
      ``@atlas:feature`` annotations; it surfaces decorator names as edges
      and lets the Go-side annotation parser handle the rest.
"""

from __future__ import annotations

import argparse
import ast
import json
import os
import sys
from dataclasses import dataclass, field
from typing import Iterable


# Directories never walked. Mirrors the TS scanner's DEFAULT_SKIP_DIRS but
# tuned for Python ecosystems: venv folders, bytecode caches, packager
# outputs, common JS-monorepo subtrees (atlas is polyglot).
DEFAULT_SKIP_DIRS: frozenset[str] = frozenset(
    {
        ".git",
        ".venv",
        "venv",
        "env",
        "__pycache__",
        "node_modules",
        ".tox",
        ".mypy_cache",
        ".pytest_cache",
        ".ruff_cache",
        "dist",
        "build",
        ".eggs",
    }
)


# ---------------------------------------------------------------------------
# Wire-format dataclasses — JSON-stable; the Go layer asserts these names.
# ---------------------------------------------------------------------------


@dataclass
class _Node:
    id: str
    kind: str
    file: str
    line: int
    doc: str = ""


@dataclass
class _Edge:
    from_: str
    to: str
    kind: str

    def to_json(self) -> dict[str, str]:
        # `from` is a Python keyword — translate at the wire boundary.
        return {"from": self.from_, "to": self.to, "kind": self.kind}


@dataclass
class _FileMeta:
    path: str
    syntax_error: str = ""


@dataclass
class _Output:
    nodes: list[_Node] = field(default_factory=list)
    edges: list[_Edge] = field(default_factory=list)
    files: list[_FileMeta] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    files_scanned: int = 0
    syntax_failures: int = 0


# ---------------------------------------------------------------------------
# AST walk
# ---------------------------------------------------------------------------


def _decorator_name(node: ast.expr) -> str:
    """Return the source form of a decorator expression.

    ``@foo`` -> ``"foo"``; ``@a.b.c`` -> ``"a.b.c"``; ``@cache(maxsize=8)``
    -> ``"cache(maxsize=8)"``. ``ast.unparse`` (Python 3.9+) handles
    every shape; the 3.8 fallback walks ``Attribute`` chains by hand.
    """
    try:
        return ast.unparse(node)
    except AttributeError:
        # Python 3.8 lacks ast.unparse; build a best-effort dotted name.
        if isinstance(node, ast.Name):
            return node.id
        if isinstance(node, ast.Attribute):
            parts: list[str] = []
            cur: ast.AST | None = node
            while isinstance(cur, ast.Attribute):
                parts.append(cur.attr)
                cur = cur.value
            if isinstance(cur, ast.Name):
                parts.append(cur.id)
            return ".".join(reversed(parts))
        return type(node).__name__


def _callee_string(node: ast.expr) -> str:
    """Render an ``ast.Call.func`` as a stable string for edge labels.

    Mirrors ``_decorator_name`` semantics — Python's dynamic dispatch
    means we cannot resolve ``obj.foo()`` to a SymbolID statically, but
    we CAN emit a stable string the consumer can grep / pattern-match.
    """
    return _decorator_name(node)


def _method_flavor(decorators: list[str]) -> str:
    """Classify a method by its decorator list.

    Returns ``"classmethod"`` / ``"staticmethod"`` / ``"instance"`` (the
    default for any method whose decorator list lacks the two well-known
    builtin decorators).
    """
    deco_set = {d.split("(")[0] for d in decorators}
    if "classmethod" in deco_set:
        return "classmethod"
    if "staticmethod" in deco_set:
        return "staticmethod"
    return "instance"


def _docstring(node: ast.AST) -> str:
    """Extract the first-line docstring from a def/class node."""
    if not isinstance(
        node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef, ast.Module)
    ):
        return ""
    doc = ast.get_docstring(node, clean=True) or ""
    # Bound the docstring at one line so the symbol payload stays compact;
    # the graph store can hold long-form docs later if there's demand.
    return doc.splitlines()[0] if doc else ""


def _module_id_from_relpath(rel_path: str) -> str:
    """Derive the canonical module id from a project-root-relative path.

    ``pkg/sub/mod.py``      -> ``"pkg.sub.mod"``
    ``pkg/sub/__init__.py`` -> ``"pkg.sub"``

    This mirrors Python's own import semantics so atlas's cross-file
    resolution stays consistent with what an interpreter would see.
    """
    rel = rel_path.replace("\\", "/")
    if rel.endswith("/__init__.py"):
        rel = rel[: -len("/__init__.py")]
    elif rel.endswith(".py"):
        rel = rel[: -len(".py")]
    return rel.replace("/", ".")


def _walk_module(
    tree: ast.Module,
    rel_path: str,
    module_id: str,
    out: _Output,
) -> None:
    """Walk one module's AST and append discoveries to ``out``."""
    # Track the current container so nested defs render as
    # "module.outer.inner" — flat names would collide across modules.
    _walk_body(tree.body, rel_path, module_id, out, parent_id=module_id)


def _params_doc(args: ast.arguments) -> str:
    """Render a function's parameters as a single-line annotation."""
    parts: list[str] = []
    # PEP 570 positional-only goes first (3.8+).
    for a in args.posonlyargs:
        parts.append(_arg_repr(a))
    for a in args.args:
        parts.append(_arg_repr(a))
    if args.vararg:
        parts.append("*" + _arg_repr(args.vararg))
    for a in args.kwonlyargs:
        parts.append(_arg_repr(a))
    if args.kwarg:
        parts.append("**" + _arg_repr(args.kwarg))
    return ", ".join(parts)


def _arg_repr(a: ast.arg) -> str:
    if a.annotation is not None:
        try:
            return f"{a.arg}: {ast.unparse(a.annotation)}"
        except AttributeError:
            return a.arg
    return a.arg


def _walk_body(
    body: list[ast.stmt],
    rel_path: str,
    module_id: str,
    out: _Output,
    parent_id: str,
) -> None:
    """Walk a list of top-level (or nested) statements.

    ``parent_id`` is the symbol id of the enclosing scope (module id at
    the top level, class id inside a class body, function id inside a
    function body). Used to:

      * Form qualified ids (``"mod.Class.method"``, ``"mod.outer.inner"``).
      * Attach call edges to their containing function.
      * Discriminate class-body defs (=> methods) from function-body defs
        (=> nested functions).
    """
    for node in body:
        if isinstance(node, ast.ClassDef):
            _visit_class(node, rel_path, module_id, out, parent_id)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            _visit_function(
                node,
                rel_path,
                module_id,
                out,
                parent_id,
                is_method=False,
            )
        elif isinstance(node, ast.Import):
            _visit_import(node, module_id, out)
        elif isinstance(node, ast.ImportFrom):
            _visit_import_from(node, module_id, out)
        elif isinstance(node, ast.Assign):
            _visit_module_assign(node, rel_path, module_id, out, parent_id)


def _visit_class(
    node: ast.ClassDef,
    rel_path: str,
    module_id: str,
    out: _Output,
    parent_id: str,
) -> None:
    class_id = f"{parent_id}.{node.name}"
    decorators = [_decorator_name(d) for d in node.decorator_list]
    bases = [_decorator_name(b) for b in node.bases]
    doc_parts: list[str] = []
    if bases:
        doc_parts.append("bases=" + ",".join(bases))
    if decorators:
        doc_parts.append("decorators=" + ",".join(decorators))
    ds = _docstring(node)
    if ds:
        doc_parts.append(ds)
    out.nodes.append(
        _Node(
            id=class_id,
            kind="class",
            file=rel_path,
            line=node.lineno,
            doc="; ".join(doc_parts),
        )
    )
    # Inheritance edges
    for base in bases:
        out.edges.append(
            _Edge(from_=class_id, to=base, kind="inheritance"),
        )
    # Decorator edges
    for deco in decorators:
        out.edges.append(_Edge(from_=class_id, to=deco, kind="decorator"))

    # Walk class body — defs become methods, nested classes recurse.
    for child in node.body:
        if isinstance(child, ast.ClassDef):
            _visit_class(child, rel_path, module_id, out, parent_id=class_id)
        elif isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
            _visit_function(
                child,
                rel_path,
                module_id,
                out,
                parent_id=class_id,
                is_method=True,
            )


def _visit_function(
    node: ast.FunctionDef | ast.AsyncFunctionDef,
    rel_path: str,
    module_id: str,
    out: _Output,
    parent_id: str,
    is_method: bool,
) -> None:
    func_id = f"{parent_id}.{node.name}"
    decorators = [_decorator_name(d) for d in node.decorator_list]
    kind = "method" if is_method else "function"
    doc_parts: list[str] = []
    flavor = _method_flavor(decorators) if is_method else ""
    if flavor:
        doc_parts.append("flavor=" + flavor)
    params = _params_doc(node.args)
    if params:
        doc_parts.append("params=(" + params + ")")
    if node.returns is not None:
        try:
            doc_parts.append("returns=" + ast.unparse(node.returns))
        except AttributeError:
            pass
    if decorators:
        doc_parts.append("decorators=" + ",".join(decorators))
    ds = _docstring(node)
    if ds:
        doc_parts.append(ds)
    line_end = getattr(node, "end_lineno", node.lineno) or node.lineno
    if line_end and line_end != node.lineno:
        doc_parts.append(f"lines={node.lineno}-{line_end}")

    out.nodes.append(
        _Node(
            id=func_id,
            kind=kind,
            file=rel_path,
            line=node.lineno,
            doc="; ".join(doc_parts),
        )
    )
    # Decorator edges
    for deco in decorators:
        out.edges.append(_Edge(from_=func_id, to=deco, kind="decorator"))

    # Walk body: emit call edges + recurse for nested defs/classes.
    _emit_call_edges(node, func_id, out)
    _walk_body(node.body, rel_path, module_id, out, parent_id=func_id)


def _emit_call_edges(
    func: ast.FunctionDef | ast.AsyncFunctionDef,
    func_id: str,
    out: _Output,
) -> None:
    """For every ``ast.Call`` inside ``func``, emit a call edge.

    Python's dynamic dispatch means the callee string is a best-effort
    rendering (``"foo"``, ``"obj.method"``, ``"mod.helper"``) — the same
    contract the TS scanner provides for symbol-level call traceability.
    """
    for sub in ast.walk(func):
        if isinstance(sub, ast.Call):
            try:
                callee = _callee_string(sub.func)
            except Exception:  # noqa: BLE001 — defensive; render must never crash
                callee = type(sub.func).__name__
            if callee:
                out.edges.append(
                    _Edge(from_=func_id, to=callee, kind="call"),
                )


def _visit_import(node: ast.Import, module_id: str, out: _Output) -> None:
    """``import X`` and ``import X as Y`` — one edge per alias."""
    for alias in node.names:
        target = alias.name
        out.edges.append(_Edge(from_=module_id, to=target, kind="import"))


def _visit_import_from(
    node: ast.ImportFrom, module_id: str, out: _Output
) -> None:
    """``from X import Y, Z`` and relative ``from . import sibling``.

    The edge target is rendered as the fully-qualified name so a Go-side
    consumer can resolve to module + symbol with simple splitting.
    """
    # Render `from .sibling import x`     -> base = ".sibling", target = ".sibling.x"
    # Render `from . import sibling`        -> base = ".",        target = ".sibling"
    # Render `from collections import X`    -> base = "collections", target = "collections.X"
    module = node.module or ""
    level_prefix = "." * (node.level or 0)
    base = level_prefix + module  # may be ""+"" = "" for naked `import` form (impossible here)
    for alias in node.names:
        if base == "":
            target = alias.name
        elif base.endswith("."):
            # Relative `from . import x` — no extra "." separator.
            target = base + alias.name
        else:
            target = f"{base}.{alias.name}"
        out.edges.append(_Edge(from_=module_id, to=target, kind="import"))


def _visit_module_assign(
    node: ast.Assign,
    rel_path: str,
    module_id: str,
    out: _Output,
    parent_id: str,
) -> None:
    """Emit module-level UPPER_SNAKE constants as ``kind=const`` nodes.

    Skipped inside function/method bodies (parent_id != module_id) — the
    issue spec says: "Skip local variables inside functions, lambdas,
    comprehension vars. Those are noise for code exploration."
    """
    if parent_id != module_id:
        return
    for target in node.targets:
        if isinstance(target, ast.Name):
            name = target.id
            if name.isupper() and not name.startswith("_"):
                out.nodes.append(
                    _Node(
                        id=f"{module_id}.{name}",
                        kind="const",
                        file=rel_path,
                        line=node.lineno,
                    )
                )


# ---------------------------------------------------------------------------
# File walking
# ---------------------------------------------------------------------------


def _iter_python_files(
    root: str, includes: list[str], excludes: frozenset[str]
) -> Iterable[str]:
    """Yield absolute paths to ``.py`` files under ``root``.

    ``includes`` narrows the walk to specified subdirectories (relative to
    ``root``). Empty -> walk the whole project root.
    ``excludes`` is the additive deny-list on directory NAMES (matched
    against the basename of each dir as os.walk descends).
    """
    roots = [os.path.join(root, inc) for inc in includes] if includes else [root]
    for top in roots:
        if not os.path.isdir(top):
            continue
        for dirpath, dirnames, filenames in os.walk(top):
            # Mutate dirnames in-place to prune the walk.
            dirnames[:] = [
                d
                for d in dirnames
                if d not in excludes and not d.startswith(".")
                or d == "."  # never going to happen, but explicit
            ]
            for fname in filenames:
                if fname.endswith(".py"):
                    yield os.path.join(dirpath, fname)


def _scan_file(abs_path: str, rel_path: str, out: _Output) -> None:
    """Read + parse one file, dispatching its AST into ``out``."""
    try:
        with open(abs_path, "r", encoding="utf-8") as fh:
            source = fh.read()
    except (OSError, UnicodeDecodeError) as exc:
        out.warnings.append(f"pyscan: read {rel_path}: {exc}")
        out.files.append(_FileMeta(path=rel_path, syntax_error=f"read: {exc}"))
        return

    try:
        tree = ast.parse(source, filename=abs_path)
    except SyntaxError as exc:
        msg = f"line {exc.lineno}: {exc.msg}"
        out.files.append(_FileMeta(path=rel_path, syntax_error=msg))
        out.syntax_failures += 1
        return

    module_id = _module_id_from_relpath(rel_path)
    # Module-level node so atlas codebase find <module> resolves.
    doc = _docstring(tree)
    out.nodes.append(
        _Node(
            id=module_id,
            kind="module",
            file=rel_path,
            line=1,
            doc=doc,
        )
    )

    _walk_module(tree, rel_path, module_id, out)
    out.files.append(_FileMeta(path=rel_path))
    out.files_scanned += 1


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def _parse_args(argv: list[str]) -> argparse.Namespace:
    p = argparse.ArgumentParser(prog="scanner.py", add_help=True)
    p.add_argument("--root", required=True)
    p.add_argument("--include", action="append", default=[])
    p.add_argument("--exclude", action="append", default=[])
    return p.parse_args(argv)


def _emit(out: _Output) -> None:
    """Render ``out`` to stdout as a single JSON object.

    Matches the TS scanner's wire pattern: one envelope, not NDJSON, so
    the Go decoder can json.Unmarshal in one shot.
    """
    payload = {
        "nodes": [n.__dict__ for n in out.nodes],
        "edges": [e.to_json() for e in out.edges],
        "files": [
            {"path": f.path, **({"syntax_error": f.syntax_error} if f.syntax_error else {})}
            for f in out.files
        ],
        "warnings": out.warnings,
        "stats": {
            "files_scanned": out.files_scanned,
            "symbols_found": len(out.nodes),
            "edges_found": len(out.edges),
            "syntax_failures": out.syntax_failures,
        },
    }
    json.dump(payload, sys.stdout, separators=(",", ":"))


def main(argv: list[str]) -> int:
    ns = _parse_args(argv)
    root = os.path.abspath(ns.root)
    if not os.path.isdir(root):
        json.dump(
            {
                "nodes": [],
                "edges": [],
                "files": [],
                "warnings": [f"pyscan: root not a directory: {root}"],
                "stats": {
                    "files_scanned": 0,
                    "symbols_found": 0,
                    "edges_found": 0,
                    "syntax_failures": 0,
                },
            },
            sys.stdout,
            separators=(",", ":"),
        )
        return 0

    excludes = DEFAULT_SKIP_DIRS | frozenset(ns.exclude)
    out = _Output()
    for abs_path in _iter_python_files(root, ns.include, excludes):
        rel_path = os.path.relpath(abs_path, root).replace(os.sep, "/")
        _scan_file(abs_path, rel_path, out)

    _emit(out)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
