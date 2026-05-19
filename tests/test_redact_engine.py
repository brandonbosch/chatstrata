"""Tests for PresidioEngine — protocol conformance and all redaction modes."""

from __future__ import annotations

import pytest

from chatstrata.redact.base import Entity, RedactionEngine, RedactionMode, RedactionResult

try:
    from chatstrata.redact.presidio_engine import PresidioEngine

    HAS_PRESIDIO = True
except ImportError:
    HAS_PRESIDIO = False

pytestmark = pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")


@pytest.fixture(scope="module")
def engine():
    return PresidioEngine(hash_salt="test-salt-fixed")


TEXT_WITH_EMAIL = "contact me at test@example.com for details"
TEXT_WITH_KEY = "config: sk-ant-api03-" + "X" * 80
TEXT_MULTI = "email test@example.com and key sk-ant-api03-" + "Y" * 80
TEXT_CLEAN = "nothing sensitive here at all"


class TestProtocol:
    def test_isinstance_check(self, engine):
        assert isinstance(engine, RedactionEngine)

    def test_has_name(self, engine):
        assert engine.name == "presidio"


class TestDetect:
    def test_detects_email(self, engine):
        entities = engine.detect(TEXT_WITH_EMAIL)
        types = [e.type for e in entities]
        assert "EMAIL_ADDRESS" in types

    def test_detects_api_key(self, engine):
        entities = engine.detect(TEXT_WITH_KEY)
        types = [e.type for e in entities]
        assert "ANTHROPIC_API_KEY" in types

    def test_returns_entity_dataclass(self, engine):
        entities = engine.detect(TEXT_WITH_EMAIL)
        assert all(isinstance(e, Entity) for e in entities)

    def test_confidence_above_threshold(self, engine):
        entities = engine.detect(TEXT_WITH_EMAIL)
        assert all(e.confidence >= 0.35 for e in entities)

    def test_empty_text_returns_empty(self, engine):
        assert engine.detect("") == []

    def test_clean_text_may_return_empty(self, engine):
        entities = engine.detect(TEXT_CLEAN)
        sensitive = [e for e in entities if e.type in (
            "ANTHROPIC_API_KEY", "EMAIL_ADDRESS", "GITHUB_TOKEN",
            "AWS_ACCESS_KEY", "CONNECTION_STRING", "JWT_TOKEN",
        )]
        assert len(sensitive) == 0

    def test_entities_sorted_by_offset(self, engine):
        entities = engine.detect(TEXT_MULTI)
        starts = [e.start for e in entities]
        assert starts == sorted(starts)


class TestDetectOnly:
    def test_does_not_modify_text(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.DETECT_ONLY)
        assert result.redacted_text == TEXT_WITH_EMAIL

    def test_has_entities(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.DETECT_ONLY)
        assert len(result.entities) > 0

    def test_returns_result_type(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.DETECT_ONLY)
        assert isinstance(result, RedactionResult)


class TestTagMode:
    def test_wraps_in_pii_tags(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.TAG)
        assert "<PII:EMAIL_ADDRESS>" in result.redacted_text
        assert "</PII:EMAIL_ADDRESS>" in result.redacted_text

    def test_preserves_entity_text_inside_tags(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.TAG)
        assert "test@example.com" in result.redacted_text


class TestMaskMode:
    def test_replaces_with_type_counter(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.MASK)
        assert "[EMAIL_ADDRESS_1]" in result.redacted_text
        assert "test@example.com" not in result.redacted_text

    def test_increments_counter_per_type(self, engine):
        text = "emails: first@a.com and second@b.com"
        result = engine.redact(text, RedactionMode.MASK)
        assert "[EMAIL_ADDRESS_1]" in result.redacted_text
        assert "[EMAIL_ADDRESS_2]" in result.redacted_text

    def test_mapping_populated(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.MASK)
        assert len(result.mapping) > 0
        assert any("test@example.com" in v for v in result.mapping.values())


class TestRemoveMode:
    def test_deletes_entity_text(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.REMOVE)
        assert "test@example.com" not in result.redacted_text
        assert "contact me at" in result.redacted_text

    def test_empty_mapping(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.REMOVE)
        assert result.mapping == {}


class TestHashMode:
    def test_produces_hash(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.HASH)
        assert "test@example.com" not in result.redacted_text
        assert len(result.redacted_text) > len(TEXT_WITH_EMAIL)

    def test_consistent_output(self, engine):
        r1 = engine.redact(TEXT_WITH_EMAIL, RedactionMode.HASH)
        r2 = engine.redact(TEXT_WITH_EMAIL, RedactionMode.HASH)
        assert r1.redacted_text == r2.redacted_text

    def test_mapping_allows_reversal(self, engine):
        result = engine.redact(TEXT_WITH_EMAIL, RedactionMode.HASH)
        assert len(result.mapping) > 0
        assert any("test@example.com" in v for v in result.mapping.values())


class TestDenyEntityTypes:
    def test_default_denies_datetime(self, engine):
        text = "meeting on 2026-05-14 at 3pm"
        types = {e.type for e in engine.detect(text)}
        assert "DATE_TIME" not in types

    def test_default_denies_organization(self, engine):
        text = "I work at Microsoft in the Azure division"
        types = {e.type for e in engine.detect(text)}
        assert "ORGANIZATION" not in types

    def test_default_denies_person(self, engine):
        text = "contact John Smith for details"
        types = {e.type for e in engine.detect(text)}
        assert "PERSON" not in types

    def test_allow_overrides_deny(self):
        from chatstrata.redact.presidio_engine import DEFAULT_DENY_ENTITY_TYPES

        engine = PresidioEngine(
            hash_salt="test-salt-fixed",
            deny_entity_types=DEFAULT_DENY_ENTITY_TYPES - {"DATE_TIME"},
        )
        text = "meeting on 2026-05-14 at noon"
        types = {e.type for e in engine.detect(text)}
        assert "DATE_TIME" in types

    def test_empty_deny_allows_all(self):
        engine = PresidioEngine(
            hash_salt="test-salt-fixed",
            deny_entity_types=frozenset(),
        )
        text = "John Smith works at Microsoft on 2026-05-14"
        types = {e.type for e in engine.detect(text)}
        assert "PERSON" in types or "ORGANIZATION" in types or "DATE_TIME" in types


class TestEdgeCases:
    def test_no_entities_returns_original(self, engine):
        result = engine.redact(TEXT_CLEAN, RedactionMode.MASK)
        assert result.redacted_text == TEXT_CLEAN

    def test_empty_input(self, engine):
        result = engine.redact("", RedactionMode.MASK)
        assert result.redacted_text == ""

    def test_multi_entity_text(self, engine):
        result = engine.redact(TEXT_MULTI, RedactionMode.MASK)
        assert "test@example.com" not in result.redacted_text
        assert "sk-ant-api03" not in result.redacted_text
