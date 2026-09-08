"""embedding — TEI client and model registry (PRD §6.4)."""

from .client import TeiClient, TeiUnavailable
from .models import ModelSpec, get_model_spec

__all__ = ["TeiClient", "TeiUnavailable", "ModelSpec", "get_model_spec"]
