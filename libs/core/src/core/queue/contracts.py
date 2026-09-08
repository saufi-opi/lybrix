"""Versioned pydantic job payloads for every Redis Stream (PRD §4.2).

Queue payloads are a wire contract between services that deploy
independently — hence pydantic validation at the edges and an explicit
``schema_version`` on every message, so an old consumer can reject (or
tolerate) a newer producer instead of misreading its fields.
"""

from __future__ import annotations

import uuid

from pydantic import BaseModel, Field

SCHEMA_VERSION = 1


class JobPayload(BaseModel):
    """Base for every queue message. ``schema_version`` bumps on any
    breaking change to a payload's fields."""

    schema_version: int = SCHEMA_VERSION


class SplitJob(JobPayload):
    """Stream ``doc.split`` — produced by the API on commit."""

    doc_id: uuid.UUID
    source_uri: str


class ParseJob(JobPayload):
    """Stream ``doc.parse`` — produced by the splitter, one per shard."""

    doc_id: uuid.UUID
    idx: int = Field(ge=0)
    page_start: int = Field(ge=1)
    page_end: int = Field(ge=1)
    source_uri: str

    def validate_pages(self) -> None:
        if self.page_end < self.page_start:
            raise ValueError(f"page_end {self.page_end} < page_start {self.page_start}")


class EmbedJob(JobPayload):
    """Stream ``doc.embed`` — produced when a document's last shard settles."""

    doc_id: uuid.UUID
