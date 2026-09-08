"""Model registry: dims and max lengths for supported embedding models
(PRD §16 open question 2 — bge-m3 assumed for v1)."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class ModelSpec:
    model_id: str
    dim: int
    max_seq_len: int
    supports_sparse: bool


_REGISTRY: dict[str, ModelSpec] = {
    "BAAI/bge-m3": ModelSpec(
        model_id="BAAI/bge-m3", dim=1024, max_seq_len=8192, supports_sparse=True
    ),
}


def get_model_spec(model_id: str) -> ModelSpec:
    try:
        return _REGISTRY[model_id]
    except KeyError:
        raise ValueError(
            f"unknown embedding model {model_id!r}; known: {sorted(_REGISTRY)}"
        ) from None
