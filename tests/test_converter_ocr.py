"""Env-configurable OCR engine selection for build_converter (PRD §6.3).

Defaults preserve the 9 Sep tuning (rapidocr + 2 threads). Every knob is
overridable through core Settings so hosts tune without code changes:
  PARSING_OCR_ENGINE=rapidocr|easyocr|tesseract|auto|none
  PARSING_OCR_LANG=en
  PARSING_OCR_TEXT_SCORE=0.5
  PARSING_TORCH_THREADS=2
  PARSING_DO_TABLE_STRUCTURE=1
  PARSING_ACCELERATOR_DEVICE=cpu
"""

from __future__ import annotations

import pytest
from core.config import Settings
from parsing.converter import build_converter


def _settings(**kw) -> Settings:
    return Settings(**kw)


def test_ocr_off_when_need_ocr_false():
    c = build_converter(need_ocr=False, settings=_settings())
    opts = c.format_to_options[next(k for k in c.format_to_options if k.value == "pdf")].pipeline_options
    assert opts.do_ocr is False


def test_rapidocr_engine_selected_by_default_for_ocr_shards():
    c = build_converter(need_ocr=True, settings=_settings())
    opts = c.format_to_options[next(k for k in c.format_to_options if k.value == "pdf")].pipeline_options
    assert opts.do_ocr is True
    assert type(opts.ocr_options).__name__ == "RapidOcrOptions"


def test_ocr_engine_env_switch(monkeypatch):
    s = _settings(parsing_ocr_engine="easyocr")
    c = build_converter(need_ocr=True, settings=s)
    opts = c.format_to_options[next(k for k in c.format_to_options if k.value == "pdf")].pipeline_options
    assert type(opts.ocr_options).__name__ == "EasyOcrOptions"


def test_ocr_engine_none_disables_ocr_even_for_ocr_shards():
    s = _settings(parsing_ocr_engine="none")
    c = build_converter(need_ocr=True, settings=s)
    opts = c.format_to_options[next(k for k in c.format_to_options if k.value == "pdf")].pipeline_options
    assert opts.do_ocr is False


def test_rapidocr_lang_and_score_from_env():
    s = _settings(parsing_ocr_lang="en", parsing_ocr_text_score=0.6)
    c = build_converter(need_ocr=True, settings=s)
    opts = c.format_to_options[next(k for k in c.format_to_options if k.value == "pdf")].pipeline_options
    assert opts.ocr_options.lang == ["en"]
    assert opts.ocr_options.text_score == 0.6


def test_table_structure_toggle():
    on = build_converter(need_ocr=False, settings=_settings(parsing_do_table_structure=True))
    off = build_converter(need_ocr=False, settings=_settings(parsing_do_table_structure=False))
    assert on.format_to_options[next(k for k in on.format_to_options if k.value == "pdf")].pipeline_options.do_table_structure is True
    assert off.format_to_options[next(k for k in off.format_to_options if k.value == "pdf")].pipeline_options.do_table_structure is False


def test_page_images_stay_off_regardless_of_env():
    # memory discipline §6.3: page-image generation is the largest win;
    # not configurable on purpose.
    s = _settings()
    c = build_converter(need_ocr=True, settings=s)
    opts = c.format_to_options[next(k for k in c.format_to_options if k.value == "pdf")].pipeline_options
    assert opts.generate_page_images is False
    assert opts.generate_picture_images is False


def test_torch_threads_applied():
    _settings(parsing_torch_threads=2)
    import torch

    assert torch.get_num_threads() <= 2


def test_invalid_engine_rejected():
    from core.config import SettingsError

    with pytest.raises((SettingsError, ValueError)):
        _settings(parsing_ocr_engine="hal9000")
