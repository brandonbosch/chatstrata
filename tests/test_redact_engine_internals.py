"""Tests for PresidioEngine internals: conflict resolution, apply edge cases, factory."""

from __future__ import annotations

from importlib.util import find_spec

import pytest

from chatstrata.redact.base import Entity, RedactionMode

HAS_PRESIDIO = find_spec("presidio_analyzer") is not None

pytestmark = pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")


@pytest.fixture(scope="module")
def engine():
    from chatstrata.redact.presidio_engine import PresidioEngine

    return PresidioEngine(hash_salt="test-salt-fixed")


class TestResolveConflicts:
    def test_no_overlap_keeps_all(self, engine):
        entities = [
            Entity(type="A", start=0, end=5, text="aaaaa", confidence=0.9),
            Entity(type="B", start=10, end=15, text="bbbbb", confidence=0.9),
        ]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 2

    def test_full_overlap_keeps_longer(self, engine):
        entities = [
            Entity(type="BROAD", start=0, end=20, text="a" * 20, confidence=0.8),
            Entity(type="NARROW", start=5, end=10, text="a" * 5, confidence=0.95),
        ]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 1
        assert result[0].type == "BROAD"

    def test_same_span_keeps_higher_confidence(self, engine):
        entities = [
            Entity(type="LOW", start=0, end=10, text="a" * 10, confidence=0.5),
            Entity(type="HIGH", start=0, end=10, text="a" * 10, confidence=0.95),
        ]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 1
        assert result[0].type == "HIGH"

    def test_partial_overlap_keeps_longer(self, engine):
        entities = [
            Entity(type="LONG", start=0, end=15, text="a" * 15, confidence=0.8),
            Entity(type="SHORT", start=10, end=20, text="b" * 10, confidence=0.9),
        ]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 1
        assert result[0].type == "LONG"

    def test_adjacent_entities_both_kept(self, engine):
        entities = [
            Entity(type="A", start=0, end=5, text="aaaaa", confidence=0.9),
            Entity(type="B", start=5, end=10, text="bbbbb", confidence=0.9),
        ]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 2

    def test_result_sorted_by_start(self, engine):
        entities = [
            Entity(type="B", start=20, end=25, text="bbbbb", confidence=0.9),
            Entity(type="A", start=0, end=5, text="aaaaa", confidence=0.9),
        ]
        result = engine._resolve_conflicts(entities)
        assert result[0].start < result[1].start

    def test_empty_list(self, engine):
        result = engine._resolve_conflicts([])
        assert result == []

    def test_single_entity(self, engine):
        entities = [Entity(type="A", start=0, end=5, text="aaaaa", confidence=0.9)]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 1

    def test_three_overlapping_keeps_one(self, engine):
        entities = [
            Entity(type="A", start=0, end=10, text="a" * 10, confidence=0.7),
            Entity(type="B", start=0, end=20, text="b" * 20, confidence=0.6),
            Entity(type="C", start=5, end=15, text="c" * 10, confidence=0.9),
        ]
        result = engine._resolve_conflicts(entities)
        assert len(result) == 1
        assert result[0].type == "B"


class TestApplyEdgeCases:
    def test_entity_at_text_start(self, engine):
        text = "test@example.com is my email"
        entities = [Entity(type="EMAIL", start=0, end=16, text="test@example.com", confidence=0.9)]
        redacted, mapping = engine._apply(text, entities, RedactionMode.MASK)
        assert redacted.startswith("[EMAIL_1]")
        assert "[EMAIL_1]" in mapping

    def test_entity_at_text_end(self, engine):
        text = "my email is test@example.com"
        entities = [Entity(type="EMAIL", start=12, end=28, text="test@example.com", confidence=0.9)]
        redacted, mapping = engine._apply(text, entities, RedactionMode.MASK)
        assert redacted.endswith("[EMAIL_1]")

    def test_adjacent_entities(self, engine):
        text = "AAAAABBBBB"
        entities = [
            Entity(type="A", start=0, end=5, text="AAAAA", confidence=0.9),
            Entity(type="B", start=5, end=10, text="BBBBB", confidence=0.9),
        ]
        redacted, mapping = engine._apply(text, entities, RedactionMode.MASK)
        assert "[A_1]" in redacted
        assert "[B_1]" in redacted
        assert len(mapping) == 2

    def test_entity_is_entire_text(self, engine):
        text = "test@example.com"
        entities = [Entity(type="EMAIL", start=0, end=16, text="test@example.com", confidence=0.9)]
        redacted, mapping = engine._apply(text, entities, RedactionMode.MASK)
        assert redacted == "[EMAIL_1]"

    def test_remove_mode_leaves_no_placeholder(self, engine):
        text = "hello test@example.com world"
        entities = [Entity(type="EMAIL", start=6, end=22, text="test@example.com", confidence=0.9)]
        redacted, mapping = engine._apply(text, entities, RedactionMode.REMOVE)
        assert redacted == "hello  world"
        assert mapping == {}

    def test_tag_mode_preserves_original(self, engine):
        text = "key is sk-ant-api03-XXXX"
        entities = [Entity(type="KEY", start=7, end=23, text="sk-ant-api03-XXXX", confidence=0.9)]
        redacted, mapping = engine._apply(text, entities, RedactionMode.TAG)
        assert "<PII:KEY>sk-ant-api03-XXXX</PII:KEY>" in redacted

    def test_hash_mode_produces_64_char_hex(self, engine):
        text = "secret"
        entities = [Entity(type="X", start=0, end=6, text="secret", confidence=0.9)]
        redacted, mapping = engine._apply(text, entities, RedactionMode.HASH)
        assert len(redacted) == 64
        assert all(c in "0123456789abcdef" for c in redacted)

    def test_multiple_same_type_numbering(self, engine):
        text = "a@a.com and b@b.com and c@c.com"
        entities = [
            Entity(type="EMAIL", start=0, end=7, text="a@a.com", confidence=0.9),
            Entity(type="EMAIL", start=12, end=19, text="b@b.com", confidence=0.9),
            Entity(type="EMAIL", start=24, end=31, text="c@c.com", confidence=0.9),
        ]
        redacted, mapping = engine._apply(text, entities, RedactionMode.MASK)
        assert "[EMAIL_1]" in redacted
        assert "[EMAIL_2]" in redacted
        assert "[EMAIL_3]" in redacted

    def test_mixed_types_numbering(self, engine):
        text = "email: a@a.com key: AKIA1234567890123456 email: b@b.com"
        entities = [
            Entity(type="EMAIL", start=7, end=14, text="a@a.com", confidence=0.9),
            Entity(type="KEY", start=20, end=40, text="AKIA1234567890123456", confidence=0.9),
            Entity(type="EMAIL", start=48, end=55, text="b@b.com", confidence=0.9),
        ]
        redacted, mapping = engine._apply(text, entities, RedactionMode.MASK)
        assert "[EMAIL_1]" in redacted
        assert "[KEY_1]" in redacted
        assert "[EMAIL_2]" in redacted


class TestGetEngineFactory:
    def test_returns_presidio_engine(self):
        from chatstrata.redact import get_engine

        engine = get_engine()
        assert engine.name == "presidio"

    def test_passes_kwargs(self):
        from chatstrata.redact import get_engine

        engine = get_engine(score_threshold=0.9, hash_salt="custom")
        assert engine._score_threshold == 0.9
        assert engine._hash_salt == "custom"


class TestScoreThreshold:
    def test_high_threshold_filters_low_confidence(self):
        from chatstrata.redact.presidio_engine import PresidioEngine

        strict = PresidioEngine(score_threshold=0.99)
        lenient = PresidioEngine(score_threshold=0.1)
        text = "contact test@example.com for info"
        strict_entities = strict.detect(text)
        lenient_entities = lenient.detect(text)
        assert len(lenient_entities) >= len(strict_entities)


class TestHashSaltBehavior:
    def test_different_salts_produce_different_hashes(self):
        from chatstrata.redact.presidio_engine import PresidioEngine

        e1 = PresidioEngine(hash_salt="salt-one")
        e2 = PresidioEngine(hash_salt="salt-two")
        text = "my email is test@example.com"
        r1 = e1.redact(text, RedactionMode.HASH)
        r2 = e2.redact(text, RedactionMode.HASH)
        assert r1.redacted_text != r2.redacted_text

    def test_default_salt_is_random(self):
        from chatstrata.redact.presidio_engine import PresidioEngine

        e1 = PresidioEngine()
        e2 = PresidioEngine()
        assert e1._hash_salt != e2._hash_salt
