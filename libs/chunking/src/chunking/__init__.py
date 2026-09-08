"""chunking — chunk construction over stitched documents (PRD §6.4).

Pure-Python and dependency-light so it is unit-testable without torch:
token counting is pluggable (whitespace heuristic by default, a real
HF tokenizer callable in production).
"""

from .hybrid import Chunk, chunk_markdown
from .dedupe import drop_duplicate_neighbours

__all__ = ["Chunk", "chunk_markdown", "drop_duplicate_neighbours"]
