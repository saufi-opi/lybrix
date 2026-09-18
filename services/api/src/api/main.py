"""FastAPI app factory (PRD §9).

Routers are kept thin; business logic lives in libs/* or repo.py so
workers and the API share one implementation of every state transition.
"""

from __future__ import annotations

from fastapi import FastAPI

from api.routers import collections, documents, events, keys, search, system, usage


def create_app() -> FastAPI:
    app = FastAPI(
        title="rag-platform control plane",
        version="0.1.0",
        description="Document Ingestion & Retrieval Platform — PRD docs/prd.md",
    )
    app.include_router(documents.router)
    app.include_router(collections.router)
    app.include_router(search.router)
    app.include_router(keys.router)
    app.include_router(usage.router)
    app.include_router(events.router)
    app.include_router(system.router)
    return app


app = create_app()
