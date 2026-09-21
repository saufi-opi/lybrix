"""Read-endpoint auth (review minor 2): every GET route on documents and
collections declares a require_scope dependency — no 401-bypass remains.

Before the fix, list/get documents, shards and collections took only
get_session (mitigated by internal binding, but the read plane drifted
from the write plane's scope discipline).
"""

from __future__ import annotations

from fastapi import Depends

from api.deps import get_session
from api.routers import collections, documents


def _route_scopes(router) -> list[tuple[str, bool]]:
    """(path, has_require_scope) for every GET route on the router."""
    out = []
    for route in router.router.routes:
        if "GET" not in getattr(route, "methods", set()):
            continue
        dependant = getattr(route, "dependant", None)
        has_scope = False
        if dependant is not None:
            for dep in dependant.dependencies:
                call = getattr(dep, "call", None)
                # require_scope returns _dep; detect via its closure qualname
                qualname = getattr(call, "__qualname__", "")
                if qualname == "require_scope.<locals>._dep":
                    has_scope = True
        out.append((route.path, has_scope))
    return out


def test_documents_get_routes_all_gated():
    """Every documents GET route declares a require_scope dependency."""
    for path, gated in _route_scopes(documents):
        assert gated, f"{path} is not scope-gated"


def test_collections_get_routes_all_gated():
    """Every collections GET route declares a require_scope dependency."""
    for path, gated in _route_scopes(collections):
        assert gated, f"{path} is not scope-gated"


def test_gated_routes_use_get_session_too():
    """The gated reads keep the DB session dependency (scope is additive)."""
    for router in (documents, collections):
        for route in router.router.routes:
            if "GET" not in getattr(route, "methods", set()):
                continue
            dependant = getattr(route, "dependant", None)
            calls = [
                getattr(dep, "call", None) for dep in (dependant.dependencies if dependant else [])
            ]
            assert get_session in calls or Depends(get_session), route.path
