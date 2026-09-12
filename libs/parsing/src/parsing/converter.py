"""build_converter() with the PRD §6.3 pinned options.

Configuration is not optional: the PyPdfiumDocumentBackend avoids
docling-parse's bad_alloc/OOM path, and page-image generation off is the
largest single memory win. Any change here must be reflected in
docs/prd.md §6.3 and re-tested against the fixture corpus.
"""

from __future__ import annotations

from core.config import Settings, get_settings
from docling.backend.pypdfium2_backend import PyPdfiumDocumentBackend
from docling.datamodel.base_models import InputFormat
from docling.datamodel.pipeline_options import (
    EasyOcrOptions,
    PdfPipelineOptions,
    RapidOcrOptions,
    TesseractCliOcrOptions,
    TesseractOcrOptions,
)
from docling.document_converter import DocumentConverter, PdfFormatOption


def build_converter(
    need_ocr: bool,
    settings: Settings | None = None,
) -> DocumentConverter:
    s = settings or get_settings()
    import torch

    torch.set_num_threads(s.parsing_torch_threads)
    opts = PdfPipelineOptions()
    opts.accelerator_options.num_threads = s.parsing_torch_threads
    opts.accelerator_options.device = s.parsing_accelerator_device
    engine = s.parsing_ocr_engine.lower()
    if engine == "rapidocr":
        opts.ocr_options = RapidOcrOptions(
            lang=[s.parsing_ocr_lang], text_score=s.parsing_ocr_text_score
        )
    elif engine == "easyocr":
        opts.ocr_options = EasyOcrOptions(lang=[s.parsing_ocr_lang])
    elif engine == "tesseract":
        opts.ocr_options = TesseractOcrOptions(lang=s.parsing_ocr_lang)
    elif engine == "tesseract_cli":
        opts.ocr_options = TesseractCliOcrOptions(lang=s.parsing_ocr_lang)
    elif engine not in {"auto", "none"}:
        raise ValueError(f"unsupported PARSING_OCR_ENGINE: {engine}")
    opts.do_ocr = need_ocr and engine != "none"
    opts.do_table_structure = s.parsing_do_table_structure
    opts.generate_page_images = False  # largest single memory win
    opts.generate_picture_images = False
    opts.images_scale = 1.0
    return DocumentConverter(
        format_options={
            InputFormat.PDF: PdfFormatOption(
                pipeline_options=opts,
                backend=PyPdfiumDocumentBackend,  # avoids docling-parse bad_alloc
            )
        }
    )


_CONVERTER_CACHE: dict[tuple, DocumentConverter] = {}


def converter_cache_key(need_ocr: bool, s: Settings) -> tuple:
    return (
        need_ocr,
        s.parsing_ocr_engine.lower(),
        s.parsing_ocr_lang,
        s.parsing_ocr_text_score,
        s.parsing_torch_threads,
        s.parsing_do_table_structure,
        s.parsing_accelerator_device,
    )


def get_converter(
    need_ocr: bool,
    settings: Settings | None = None,
    builder=None,
) -> DocumentConverter:
    """Module-level cache: DocumentConverter init is per-process expensive
    (pipeline init + HF artifact resolution happen on first convert()). Parsers
    are recycled only after PARSER_RECYCLE_AFTER jobs, so the cache pays off
    across the process lifetime. Max 2 entries (OCR on/off).

    ``builder`` defaults to this module's build_converter; callers that keep
    their own import (workers.parser) pass it through so patch targets and
    any future local wrapping keep working."""
    s = settings or get_settings()
    key = converter_cache_key(need_ocr, s)
    conv = _CONVERTER_CACHE.get(key)
    if conv is None:
        conv = (builder or build_converter)(need_ocr=need_ocr, settings=s)
        _CONVERTER_CACHE[key] = conv
    return conv
