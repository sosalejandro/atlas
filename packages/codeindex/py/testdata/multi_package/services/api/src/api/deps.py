"""Sample API service that imports across the monorepo via the
canonical Python module path (`mypkg.db.models`), not the path-rooted
form (`packages.db.src.mypkg.db.models`) that atlas's scanner derives
from the file's repo-relative location.

Issue #15 fixture: rule (2) — canonical-Python-name suffix match —
must resolve the import edges in this file to the internal symbols
defined in packages/db/src/mypkg/db/models.py.
"""

from mypkg.db.models import Case, User


def lookup(case_id: str) -> Case:
    return Case()


def me() -> User:
    return User()
