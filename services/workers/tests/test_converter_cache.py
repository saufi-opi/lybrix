"""Tests for the module-level converter cache (get_converter).

Parser rebuilt a DocumentConverter per shard job — docling pays pipeline
init (layout-model load + HF artifact resolution) on the first convert()
of each new instance, so per-shard rebuilds wasted real CPU on hosts
parsing a shard every ~13-16s. get_converter() caches at most 2 entries
(OCR on/off) keyed by every converter-affecting setting.
"""

from __future__ import annotations

from unittest.mock import patch

from parsing.converter import _CONVERTER_CACHE, converter_cache_key, get_converter

from tests.conftest import make_settings


def setup_function(_fn):
    _CONVERTER_CACHE.clear()


def teardown_function(_fn):
    _CONVERTER_CACHE.clear()


def test_get_converter_same_key_returns_same_instance():
    s = make_settings()
    fake = object()
    with patch("parsing.converter.build_converter", return_value=fake) as build:
        a = get_converter(need_ocr=True, settings=s)
        b = get_converter(need_ocr=True, settings=s)
    assert a is b is fake
    build.assert_called_once()


def test_get_converter_different_need_ocr_builds_separately():
    s = make_settings()
    with patch(
        "parsing.converter.build_converter",
        side_effect=[object(), object()],
    ) as build:
        a = get_converter(need_ocr=True, settings=s)
        b = get_converter(need_ocr=False, settings=s)
    assert a is not b
    assert build.call_count == 2
    assert [c.kwargs["need_ocr"] for c in build.call_args_list] == [True, False]


def test_get_converter_settings_change_rebuilds():
    with patch(
        "parsing.converter.build_converter",
        side_effect=[object(), object()],
    ) as build:
        a = get_converter(need_ocr=True, settings=make_settings(parsing_ocr_engine="easyocr"))
        b = get_converter(need_ocr=True, settings=make_settings(parsing_ocr_engine="tesseract"))
    assert a is not b
    assert build.call_count == 2


def test_cache_hit_does_not_rebuild():
    s = make_settings()
    with patch("parsing.converter.build_converter", return_value=object()) as build:
        get_converter(need_ocr=False, settings=s)
        get_converter(need_ocr=False, settings=s)
        get_converter(need_ocr=False, settings=s)
    assert build.call_count == 1


def test_cache_key_covers_every_converter_setting():
    s1 = make_settings()
    s2 = make_settings(parsing_ocr_text_score=0.9)
    s3 = make_settings(parsing_torch_threads=1)
    s4 = make_settings(parsing_do_table_structure=False)
    s5 = make_settings(parsing_accelerator_device="cuda")
    s6 = make_settings(parsing_ocr_lang="de")
    base = converter_cache_key(True, s1)
    for changed in (s2, s3, s4, s5, s6):
        assert converter_cache_key(True, changed) != base
