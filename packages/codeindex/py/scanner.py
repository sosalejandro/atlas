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
      "annotations": [{"kind", "id", "file", "line", "raw"}, ...],
      "files": [{"path", "syntax_error"}, ...],
      "warnings": ["..."],
      "stats": {"files_scanned", "symbols_found", "edges_found",
                "annotations_found", "syntax_failures"}
    }

The Go orchestrator maps node.kind onto shared.SymbolKind and emits
graph.Edge values verbatim. All file paths are repo-relative
(forward-slash) so the persisted shared.FilePosition is portable.

Annotations come in two recognition modes (both supported):

* **Comment-style** — ``# @atlas:<kind> <id> [tags...]`` on the line
  IMMEDIATELY above a ``def`` / ``class`` declaration. Mirrors Go's
  ``// @atlas:feature ...`` convention.
* **Decorator-style** — ``@atlas.feature("id")`` (or ``@feature("id")``
  when the helper is imported as ``from atlas import feature``).
  Atlas treats the decorator as no-op runtime sugar and reads the
  feature id statically.

Class-level annotations PROPAGATE to every method defined inside the
class: scanning a class with ``@atlas:feature billing.subscribe``
materializes one annotation record per method, anchored at the method's
own line so the Go-side ``LookupSymbolAtOrAfterLine`` resolves to the
method symbol. This resolves the v0.3.0 gotcha #3 documented in
``docs/languages/py.md``.

Usage::

    python3 scanner.py --root <project-root>
                       [--include <dir>]...
                       [--exclude <dir>]...

The CLI flags are forwarded by the Go layer. With no --include, the
scanner walks the entire project root, skipping the always-excluded
directories listed in ``DEFAULT_SKIP_DIRS``.

Constraints:
    * Pure stdlib (``ast``, ``json``, ``sys``, ``os``, ``re``,
      ``argparse``).
    * No pip dependencies — atlas's value prop is "just works once
      python3 is on PATH".
    * Comment-style annotations are also surfaced by the Go-side
      ``packages/codeindex/annotations`` parser (which sees every ``#``
      comment in every ``.py`` file). The Python scanner emits them
      again because (a) decorator-form is invisible to the comment
      parser, and (b) class-level propagation requires AST-aware
      anchoring that only the scanner has. Duplicate records are
      idempotent at the ``feature_symbols`` link layer.
"""

from __future__ import annotations

import argparse
import ast
import json
import os
import re
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
class _Annotation:
    """One ``@atlas:<kind> <id>`` record discovered by the AST walker.

    ``raw`` carries the source-form payload so downstream consumers can
    re-render the annotation for diagnostics. Tags (``#tag``, ``key=val``)
    are preserved in ``raw`` even though the Go-side store schema flattens
    them onto the ``annotations`` row.
    """

    kind: str  # e.g. "feature"; matches annotations.Kinds keys
    id: str
    file: str
    line: int
    raw: str = ""


@dataclass
class _Output:
    nodes: list[_Node] = field(default_factory=list)
    edges: list[_Edge] = field(default_factory=list)
    annotations: list[_Annotation] = field(default_factory=list)
    files: list[_FileMeta] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    files_scanned: int = 0
    syntax_failures: int = 0


# ---------------------------------------------------------------------------
# Annotation extraction
# ---------------------------------------------------------------------------


# Comment-style annotation grammar — mirrors the Go-side
# ``atlasAnnotationRe`` in packages/codeindex/annotations/parser.go so a
# Python project parses identically whichever code path sees the comment
# first.
#
# Example matches::
#
#     # @atlas:feature billing.subscribe
#     # @atlas:owner alice
#     # @atlas:feature billing.subscribe #real step=1
#
# Group 1 is the kind ("feature", "owner", …). Group 2 is the rest of the
# line (id + optional tags). The scanner forwards group 2 verbatim as the
# annotation record's ``raw`` field and extracts the first whitespace-
# separated token as the id.
_ATLAS_COMMENT_RE = re.compile(
    r"^\s*#\s*@atlas:([a-zA-Z][a-zA-Z0-9_-]*)\s+(.+?)\s*$"
)


def _first_id_token(payload: str) -> str:
    """Return the first whitespace-separated token that is not a ``#tag`` or
    ``key=value`` pair. Mirrors the Go-side ``splitIDsAndTags`` semantics."""
    for tok in payload.split():
        if tok.startswith("#"):
            continue
        if "=" in tok and not tok.startswith("="):
            # Looks like a key=value tag; tags follow ids so any "=" means
            # we've already seen all the ids — abort.
            return ""
        return tok
    return ""


def _extract_comment_annotation(
    node: ast.AST, source_lines: list[str], rel_path: str
) -> _Annotation | None:
    """Inspect the line immediately above ``node`` for a comment-style
    ``# @atlas:<kind> <id>`` annotation.

    Returns ``None`` when the line above is blank, a different comment,
    or non-comment source.

    Decorators sit BETWEEN the comment and the ``def``/``class`` keyword
    in source, so for decorated symbols the comment-bearing line is
    ``decorator_list[0].lineno - 1`` rather than ``node.lineno - 1``.
    Defensive against missing decorator metadata (returns ``None`` when
    the calculation would underflow).
    """
    anchor_line = node.lineno
    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
        if node.decorator_list:
            first_dec = node.decorator_list[0]
            anchor_line = min(anchor_line, getattr(first_dec, "lineno", anchor_line))
    idx = anchor_line - 2  # source_lines is 0-indexed; line N is index N-1
    if idx < 0 or idx >= len(source_lines):
        return None
    m = _ATLAS_COMMENT_RE.match(source_lines[idx])
    if not m:
        return None
    kind = m.group(1)
    payload = m.group(2).strip()
    fid = _first_id_token(payload)
    if not fid:
        return None
    return _Annotation(
        kind=kind, id=fid, file=rel_path, line=node.lineno, raw=payload
    )


def _extract_decorator_annotation(
    node: ast.FunctionDef | ast.AsyncFunctionDef | ast.ClassDef,
    rel_path: str,
) -> _Annotation | None:
    """Inspect ``node.decorator_list`` for a ``@atlas.feature("id")`` or
    ``@feature("id")`` (when imported as ``from atlas import feature``)
    decorator.

    Returns the annotation on the first match and stops. Multiple
    ``@atlas.feature(...)`` decorators on the same symbol would all
    resolve to the same kind anyway; if a future use case wants
    multi-id-per-symbol, this is the seam to extend.
    """
    for dec in node.decorator_list:
        if not isinstance(dec, ast.Call):
            continue
        kind = _decorator_atlas_kind(dec.func)
        if kind is None:
            continue
        if not dec.args:
            continue
        first = dec.args[0]
        fid = _string_constant(first)
        if fid is None or not fid:
            continue
        return _Annotation(
            kind=kind,
            id=fid,
            file=rel_path,
            line=node.lineno,
            raw=f"{fid}",
        )
    return None


def _decorator_atlas_kind(func: ast.expr) -> str | None:
    """Match ``atlas.<kind>`` and ``<kind>`` decorator call shapes against
    the closed set of atlas annotation kinds.

    Returns the canonical kind string on match (``feature``, ``bc``,
    ``aggregate-service`` etc.), ``None`` otherwise. Only kinds that
    take a single string-id payload are recognised here; free-form kinds
    like ``owner`` / ``deprecated`` are intentionally NOT decorator-
    addressable because the runtime ergonomics are awkward (a string
    argument is the wrong shape for "alice@team.com" or a free-text
    deprecation reason).

    Python identifiers cannot contain ``-`` so the kebab-case wire kinds
    (``aggregate-service``, ``event-emit``, ``outbox-publish``) are
    reached through their snake_case Python aliases
    (``aggregate_service`` etc.). The mapping mirrors the helper module
    shipped at ``assets/python/atlas.py``.
    """
    # Python-attribute name -> canonical wire kind.
    decoratable = {
        "feature": "feature",
        "contract": "contract",
        "bc": "bc",
        "aggregate": "aggregate",
        "aggregate_service": "aggregate-service",
        "saga": "saga",
        "consumer": "consumer",
        "event_emit": "event-emit",
        "outbox_publish": "outbox-publish",
    }
    if isinstance(func, ast.Attribute):
        # @atlas.feature(...) — value must be ast.Name(id='atlas').
        if isinstance(func.value, ast.Name) and func.value.id == "atlas":
            return decoratable.get(func.attr)
        return None
    if isinstance(func, ast.Name):
        # @feature(...) — valid when imported as
        # `from atlas import feature`. We can't always prove the import
        # statically (a project could shadow the name), but accepting
        # the bare name is the documented convention.
        return decoratable.get(func.id)
    return None


def _string_constant(node: ast.expr) -> str | None:
    """Return the string value of a Constant/Str AST node, or None.

    Python 3.8 deprecated ``ast.Str`` in favour of ``ast.Constant``; the
    helper accepts either so scanner.py runs on the minimum supported
    Python version.
    """
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    # ast.Str path is gone in 3.12+; keep for 3.8 compatibility.
    if hasattr(ast, "Str") and isinstance(node, ast.Str):  # type: ignore[attr-defined]
        return node.s  # type: ignore[attr-defined]
    return None


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
    source_lines: list[str],
    out: _Output,
) -> None:
    """Walk one module's AST and append discoveries to ``out``.

    ``source_lines`` is the file's text split on ``\\n``; the AST walker
    consults it when looking for comment-style annotations on the line
    above each ``def``/``class`` (the AST itself does not retain
    comments).
    """
    # Track the current container so nested defs render as
    # "module.outer.inner" — flat names would collide across modules.
    _walk_body(
        tree.body,
        rel_path,
        module_id,
        source_lines,
        out,
        parent_id=module_id,
        inherited_annotations=[],
    )


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
    source_lines: list[str],
    out: _Output,
    parent_id: str,
    inherited_annotations: list[_Annotation],
) -> None:
    """Walk a list of top-level (or nested) statements.

    ``parent_id`` is the symbol id of the enclosing scope (module id at
    the top level, class id inside a class body, function id inside a
    function body). Used to:

      * Form qualified ids (``"mod.Class.method"``, ``"mod.outer.inner"``).
      * Attach call edges to their containing function.
      * Discriminate class-body defs (=> methods) from function-body defs
        (=> nested functions).

    ``inherited_annotations`` carries class-level annotations DOWN to
    each method so AC #6 (class-level propagation) holds without an
    extra pass.
    """
    for node in body:
        if isinstance(node, ast.ClassDef):
            _visit_class(node, rel_path, module_id, source_lines, out, parent_id)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            _visit_function(
                node,
                rel_path,
                module_id,
                source_lines,
                out,
                parent_id,
                is_method=False,
                inherited_annotations=inherited_annotations,
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
    source_lines: list[str],
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

    # Annotations on the class itself. We collect EVERY hit (comment +
    # decorator forms can co-exist on the same symbol) so a project that
    # uses both styles in a transition window still resolves to a single
    # `feature_symbols` link via the store's INSERT-OR-IGNORE upsert.
    class_anns: list[_Annotation] = []
    comment_ann = _extract_comment_annotation(node, source_lines, rel_path)
    if comment_ann is not None:
        class_anns.append(comment_ann)
    decorator_ann = _extract_decorator_annotation(node, rel_path)
    if decorator_ann is not None:
        class_anns.append(decorator_ann)
    out.annotations.extend(class_anns)

    # Class-level annotations propagate to each method body (AC #6).
    # Each propagated record is anchored at the method's line so the
    # Go-side LookupSymbolAtOrAfterLine resolves to the method symbol.
    # If a method also carries its own annotation, both get emitted;
    # the store's idempotent upsert collapses them into one link.
    inherited: list[_Annotation] = list(class_anns)

    # Walk class body — defs become methods, nested classes recurse.
    for child in node.body:
        if isinstance(child, ast.ClassDef):
            _visit_class(
                child, rel_path, module_id, source_lines, out, parent_id=class_id
            )
        elif isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
            _visit_function(
                child,
                rel_path,
                module_id,
                source_lines,
                out,
                parent_id=class_id,
                is_method=True,
                inherited_annotations=inherited,
            )


def _visit_function(
    node: ast.FunctionDef | ast.AsyncFunctionDef,
    rel_path: str,
    module_id: str,
    source_lines: list[str],
    out: _Output,
    parent_id: str,
    is_method: bool,
    inherited_annotations: list[_Annotation],
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

    # Annotation extraction — own (comment + decorator) PLUS any
    # inherited records propagated from the enclosing class. Inherited
    # records are re-anchored at this symbol's line so the Go-side
    # LookupSymbolAtOrAfterLine resolves to the method, not the class.
    own_ann_comment = _extract_comment_annotation(node, source_lines, rel_path)
    if own_ann_comment is not None:
        out.annotations.append(own_ann_comment)
    own_ann_decorator = _extract_decorator_annotation(node, rel_path)
    if own_ann_decorator is not None:
        out.annotations.append(own_ann_decorator)
    for ann in inherited_annotations:
        out.annotations.append(
            _Annotation(
                kind=ann.kind,
                id=ann.id,
                file=rel_path,
                line=node.lineno,
                raw=ann.raw,
            )
        )

    # Walk body: emit call edges + recurse for nested defs/classes.
    # Nested defs inside a function body do NOT inherit class
    # annotations — closure-captured callbacks aren't part of the class
    # API surface.
    _emit_call_edges(node, func_id, out)
    _walk_body(
        node.body,
        rel_path,
        module_id,
        source_lines,
        out,
        parent_id=func_id,
        inherited_annotations=[],
    )


def _emit_call_edges(
    func: ast.FunctionDef | ast.AsyncFunctionDef,
    func_id: str,
    out: _Output,
) -> None:
    """For every ``ast.Call`` inside ``func`` *but not inside a nested
    ``def`` / ``async def`` / ``class``*, emit a call edge.

    Python's dynamic dispatch means the callee string is a best-effort
    rendering (``"foo"``, ``"obj.method"``, ``"mod.helper"``) — the same
    contract the TS scanner provides for symbol-level call traceability.

    Issue #18: ``ast.walk`` descends into every child node including
    nested function bodies, which would attribute a nested ``def``'s
    calls to the enclosing function as well. The recursion in
    ``_walk_body`` already emits call edges for nested defs/methods with
    their own ``func_id`` — duplicating them under the outer ``func_id``
    breaks caller identity for trace + blast-radius queries. We walk only
    the function's *own* body, stopping descent at any new function or
    class scope, while still visiting lambdas and comprehensions
    (Python-3 list/set/dict/generator scopes that do NOT introduce a
    nameable symbol in the symbol table).
    """
    for call in _iter_own_scope_calls(func):
        try:
            callee = _callee_string(call.func)
        except Exception:  # noqa: BLE001 — defensive; render must never crash
            callee = type(call.func).__name__
        if callee:
            out.edges.append(
                _Edge(from_=func_id, to=callee, kind="call"),
            )


# AST node types that introduce a new *named* scope (a symbol that ends
# up in the Atlas symbol table). Recursion stops at these — they get
# their own ``_emit_call_edges`` invocation via ``_walk_body`` and must
# not double-count calls under the enclosing scope.
#
# Lambdas + comprehensions are deliberately NOT in this set: they
# introduce a Python scope but no nameable symbol, so any calls they
# make are still attributed to the enclosing def/method (the only place
# a human would look for "who calls X").
_OWN_SCOPE_STOP_TYPES: tuple[type, ...] = (
    ast.FunctionDef,
    ast.AsyncFunctionDef,
    ast.ClassDef,
)


def _iter_own_scope_calls(
    func: ast.FunctionDef | ast.AsyncFunctionDef,
) -> Iterable[ast.Call]:
    """Yield every ``ast.Call`` inside ``func``'s own scope.

    Walks ``func.body`` (and inline lambda / comprehension bodies) but
    halts at any nested ``def`` / ``async def`` / ``class`` so calls
    made by the inner scope are NOT yielded here. The inner scope gets
    its own attribution pass via ``_walk_body`` -> ``_visit_function``.
    """

    def _walk(node: ast.AST) -> Iterable[ast.Call]:
        if isinstance(node, ast.Call):
            yield node
        for child in ast.iter_child_nodes(node):
            if isinstance(child, _OWN_SCOPE_STOP_TYPES):
                # New named scope — its calls belong to it, not us.
                continue
            yield from _walk(child)

    for stmt in func.body:
        if isinstance(stmt, _OWN_SCOPE_STOP_TYPES):
            # Top-level nested def/class inside the function body — same
            # rationale as above. The recursion in _walk_body picks it up.
            continue
        yield from _walk(stmt)


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

    # Source-line view used by the comment-annotation extractor. We keep
    # this scoped to _scan_file so the lifetime ends when the function
    # returns — no need to retain the source in _Output.
    source_lines = source.splitlines()
    _walk_module(tree, rel_path, module_id, source_lines, out)
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
        "annotations": [a.__dict__ for a in out.annotations],
        "files": [
            {"path": f.path, **({"syntax_error": f.syntax_error} if f.syntax_error else {})}
            for f in out.files
        ],
        "warnings": out.warnings,
        "stats": {
            "files_scanned": out.files_scanned,
            "symbols_found": len(out.nodes),
            "edges_found": len(out.edges),
            "annotations_found": len(out.annotations),
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
                "annotations": [],
                "files": [],
                "warnings": [f"pyscan: root not a directory: {root}"],
                "stats": {
                    "files_scanned": 0,
                    "symbols_found": 0,
                    "edges_found": 0,
                    "annotations_found": 0,
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
