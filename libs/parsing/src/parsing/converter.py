"""build_converter() with the PRD §6.3 pinned options.

Configuration is not optional: the PyPdfiumDocumentBackend avoids
docling-parse's bad_alloc/OOM path, and page-image generation off is the
largest single memory win. Any change here must be reflected in
docs/prd.md §6.3 and re-tested against the fixture corpus.
"""

from __future__ import annotations

from docling.backend.pypdfium2_backend import PyPdfiumDocumentBackend
from docling.datamodel.base_models import InputFormat
from docling.datamodel.pipeline_options import PdfPipelineOptions
from docling.document_converter import DocumentConverter, PdfFormatOption


def build_converter(need_ocr: bool) -> DocumentConverter:
    opts = PdfPipelineOptions()
    opts.do_ocr = need_ocr
    opts.do_table_structure = True
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
