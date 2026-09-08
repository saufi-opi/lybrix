"""OCR gate: text-layer detection before Docling (PRD §6.3 step 3).

Per-page OCR combined with table detection and cell matching is where
memory expands non-linearly, so OCR stays off for the ~90% of pages
that have a text layer. Mean chars/page < threshold ⇒ needs_ocr.
"""

from __future__ import annotations

from dataclasses import dataclass

import pypdfium2 as pdfium
from core.config import Settings, get_settings


@dataclass(frozen=True)
class OCRVerdict:
    needs_ocr: bool
    mean_chars_per_page: float


def page_text_chars(pdf_path: str, page_start: int, page_end: int) -> list[int]:
    """Character counts of each page's text layer (1-based, inclusive)."""
    counts: list[int] = []
    pdf = pdfium.PdfDocument(pdf_path)
    try:
        for i in range(page_start - 1, min(page_end, len(pdf))):
            page = pdf[i]
            tp = page.get_textpage()
            try:
                counts.append(len(tp.get_text_bounded()))
            finally:
                tp.close()
                page.close()
    finally:
        pdf.close()
    return counts


def needs_ocr(
    pdf_path: str,
    page_start: int,
    page_end: int,
    settings: Settings | None = None,
) -> OCRVerdict:
    s = settings or get_settings()
    counts = page_text_chars(pdf_path, page_start, page_end)
    if not counts:
        return OCRVerdict(needs_ocr=True, mean_chars_per_page=0.0)
    mean = sum(counts) / len(counts)
    return OCRVerdict(needs_ocr=mean < s.ocr_min_chars_per_page, mean_chars_per_page=mean)
