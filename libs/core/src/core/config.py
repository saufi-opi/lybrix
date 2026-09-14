"""Single source of truth for all rag-platform configuration (PRD §12).

Pydantic-settings BaseModel: every configurable value is read from the
environment through this module — nothing else in the codebase reads
``os.environ`` directly or hardcodes a host/port/model name.
"""

from __future__ import annotations

from functools import lru_cache

from pydantic import Field, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class SettingsError(ValueError):
    """Raised when an env var is missing or invalid. Names the variable."""


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # -- control plane -----------------------------------------------------
    database_url: str = Field(
        default="postgresql+psycopg://rag:rag@localhost:5432/rag",
        description="SQLAlchemy URL; compose injects the real one.",
    )
    redis_url: str = Field(default="redis://localhost:6379/0")
    s3_endpoint: str = Field(default="http://localhost:9000")
    s3_access_key: str = Field(default="minioadmin")
    s3_secret_key: str = Field(default="minioadmin")
    s3_bucket_raw: str = Field(default="raw")
    s3_bucket_parsed: str = Field(default="parsed")
    qdrant_url: str = Field(default="http://localhost:6333")
    qdrant_api_key: str | None = Field(default=None)

    # -- embedding plane (PRD §4.3: ingest and query TEIs never share one)
    tei_ingest_url: str = Field(default="http://localhost:8081")
    tei_query_url: str = Field(default="http://localhost:8082")
    embed_backend: str = Field(default="tei", description="tei or ollama")
    embed_model: str = Field(default="BAAI/bge-m3")
    embed_dim: int = Field(default=1024)
    embed_batch_size: int = Field(default=48)
    embed_concurrency: int = Field(default=6)
    # Chunk text is truncated client-side before POST /embed. bge-m3's context
    # is 8192 tokens; a pathological chunk (e.g. one giant whitespace-free
    # blob) would otherwise 400 the whole batch and poison the embed job.
    # 0 disables truncation. chars-per-token ~3 -> 16k chars << 8192 tokens.
    embed_truncate_chars: int = Field(default=16000)

    # -- splitting (PRD §6.2)
    shard_pages: int = Field(default=20, ge=4, le=200)

    # -- parsing (PRD §6.3 memory discipline)
    parser_soft_rss_mb: int = Field(default=6144)
    parser_recycle_after: int = Field(default=10)
    shard_lease_seconds: int = Field(default=600)
    parser_pdf_cache_dir: str | None = Field(
        default=None,
        description=(
            "Optional host-local cache of raw source PDFs. When set, a shard job "
            "skips the MinIO download if this file already exists on the host: "
            "{cache_dir}/{doc_id}.pdf. PDFs are immutable per doc_id, so cache "
            "entries never need invalidation. Bind-mount the same host dir into "
            "every parser container on that host."
        ),
    )
    ocr_min_chars_per_page: int = Field(
        default=20,
        description="Mean chars/page below this marks the shard needs_ocr.",
    )
    parsing_ocr_engine: str = Field(
        default="rapidocr",
        description="OCR engine for text-layer-less shards: rapidocr|easyocr|tesseract|tesseract_cli|auto|none.",
    )
    parsing_ocr_lang: str = Field(
        default="en",
        description="OCR language code passed to the engine (rapidocr/easyocr).",
    )
    parsing_ocr_text_score: float = Field(
        default=0.5,
        description="Minimum OCR text confidence (rapidocr text_score).",
    )
    parsing_torch_threads: int = Field(
        default=2,
        ge=1,
        description="torch.set_num_threads in parser processes.",
    )
    parsing_do_table_structure: bool = Field(
        default=True,
        description="Docling table structure extraction (disable on RAM-starved hosts).",
    )
    parsing_accelerator_device: str = Field(
        default="cpu",
        description="Docling accelerator: cpu|cuda|mps|auto.",
    )

    # -- queues / backpressure (PRD §6.1)
    max_parse_backlog: int = Field(default=2000)
    worker_prefetch: int = Field(default=1)
    worker_concurrency: int = Field(default=1)

    # -- janitor (PRD §6.6)
    janitor_interval_s: int = Field(default=30)
    stuck_minutes: int = Field(default=60)
    max_shard_attempts: int = Field(default=4)

    # -- MCP / retrieval (PRD §7)
    mcp_host: str = Field(default="0.0.0.0")
    mcp_port: int = Field(default=8430)
    mcp_api_key: str = Field(default="")
    search_default_top_k: int = Field(default=8, ge=1)
    search_max_top_k: int = Field(default=25, ge=1)
    read_pages_max: int = Field(default=30, ge=1)

    # -- misc
    log_level: str = Field(default="INFO")

    @model_validator(mode="after")
    def _validate(self) -> Settings:
        valid_ocr = {"rapidocr", "easyocr", "tesseract", "tesseract_cli", "auto", "none"}
        if self.parsing_ocr_engine.lower() not in valid_ocr:
            raise SettingsError(
                f"PARSING_OCR_ENGINE must be one of {sorted(valid_ocr)}"
            )
        if self.embed_batch_size < 1:
            raise SettingsError("EMBED_BATCH_SIZE must be >= 1")
        if self.embed_truncate_chars < 0:
            raise SettingsError("EMBED_TRUNCATE_CHARS must be >= 0")
        if self.search_default_top_k > self.search_max_top_k:
            raise SettingsError(
                f"SEARCH_DEFAULT_TOP_K ({self.search_default_top_k}) must be "
                f"<= SEARCH_MAX_TOP_K ({self.search_max_top_k})"
            )
        return self


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Module-level singleton accessor."""
    return Settings()
