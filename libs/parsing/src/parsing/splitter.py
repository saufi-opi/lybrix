"""Page-range sharding with chapter-aligned bounds (PRD §6.2).

A book is never a unit of work; a 16–24 page shard is (§1.1). Shard
boundaries snap to PDF bookmarks where available — chapters rarely
split a table or paragraph, so this eliminates most cross-boundary
chunk damage — falling back to fixed windows with a 1-page overlap the
embedder later dedupes by hash.
"""

from __future__ import annotations

from dataclasses import dataclass

from core.config import Settings, get_settings


@dataclass(frozen=True)
class ShardBound:
    idx: int
    page_start: int  # inclusive, 1-based
    page_end: int  # inclusive


def fixed_bounds(
    page_count: int,
    shard_pages: int,
    overlap: int = 1,
) -> list[ShardBound]:
    """Fixed windows with a trailing 1-page overlap between consecutive
    shards. The embedder dedupes overlapping content by chunk hash."""
    if page_count < 1:
        raise ValueError(f"page_count must be >= 1, got {page_count}")
    if shard_pages < 1:
        raise ValueError(f"shard_pages must be >= 1, got {shard_pages}")

    bounds: list[ShardBound] = []
    start = 1
    idx = 0
    while start <= page_count:
        end = min(start + shard_pages - 1, page_count)
        bounds.append(ShardBound(idx=idx, page_start=start, page_end=end))
        idx += 1
        start = end + 1 - overlap if end < page_count else end + 1
        # The overlap shifts the next start back one page, but never
        # before the current start (guard against shard_pages=1 loops).
        if start <= bounds[-1].page_start and end < page_count:
            start = bounds[-1].page_start + 1
    return bounds


def chapter_aligned_bounds(
    outline: list[tuple[int, str]],
    page_count: int,
    settings: Settings | None = None,
) -> list[ShardBound]:
    """Snap shard boundaries to bookmarks (1-based target pages).

    ``outline`` is [(page, title), ...] in document order. Between two
    consecutive chapter starts we emit fixed windows of shard_pages;
    the final window of each chapter range is allowed to run short
    rather than spill into the next chapter. A leading preamble (pages
    before the first bookmark) is sharded with fixed bounds too.
    """
    s = settings or get_settings()
    shard_pages = s.shard_pages
    clean = sorted({(max(1, min(p, page_count)), t) for p, t in outline})

    bounds: list[ShardBound] = []
    idx = 0
    prev_start = 1
    for page, _title in clean:
        # shard the range [prev_start, page-1] with fixed windows
        if page > prev_start:
            for b in fixed_bounds(page - prev_start, shard_pages, overlap=0):
                bounds.append(
                    ShardBound(
                        idx=idx,
                        page_start=b.page_start + prev_start - 1,
                        page_end=b.page_end + prev_start - 1,
                    )
                )
                idx += 1
        prev_start = page
    if prev_start <= page_count:
        for b in fixed_bounds(page_count - prev_start + 1, shard_pages, overlap=0):
            bounds.append(
                ShardBound(
                    idx=idx,
                    page_start=b.page_start + prev_start - 1,
                    page_end=b.page_end + prev_start - 1,
                )
            )
            idx += 1

    # Degenerate outline (e.g. every bookmark on page 1) can yield zero
    # or single-page shards only; fall back to plain fixed bounds.
    if not bounds or any(b.page_end < b.page_start for b in bounds):
        return fixed_bounds(page_count, shard_pages)
    return bounds


def extract_outline(pdf_path: str) -> list[tuple[int, str]]:
    """Best-effort bookmark extraction via pypdfium2; [] when absent.

    Kept tolerant: a broken outline must never fail the split stage —
    fixed bounds are always a valid fallback.
    """
    try:
        import pypdfium2 as pdfium

        pdf = pdfium.PdfDocument(pdf_path)
        try:
            out: list[tuple[int, str]] = []
            for bm in pdf.get_toc():
                if bm.page_index is not None:
                    out.append((bm.page_index + 1, bm.title or ""))
            return out
        finally:
            pdf.close()
    except Exception:
        return []
