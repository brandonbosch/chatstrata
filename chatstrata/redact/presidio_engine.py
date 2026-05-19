"""Presidio-backed redaction engine."""

from __future__ import annotations

import hashlib
import secrets
import sys
from collections import defaultdict

from presidio_analyzer import AnalyzerEngine, RecognizerRegistry

from chatstrata.redact.base import Entity, RedactionMode, RedactionResult
from chatstrata.redact.recognizers import get_all_recognizers

DEFAULT_DENY_ENTITY_TYPES = frozenset({
    "DATE_TIME",
    "ORGANIZATION",
    "PERSON",
    "LOCATION",
    "NRP",
})


def _disable_tldextract_cache() -> None:
    """Keep Presidio email detection from writing a home-dir cache or fetching PSL data."""
    try:
        import tldextract.tldextract as tldextract_impl
    except ImportError:
        return

    tldextract_impl.TLD_EXTRACTOR = tldextract_impl.TLDExtract(
        cache_dir=None,
        suffix_list_urls=(),
        fallback_to_snapshot=True,
    )


class PresidioEngine:
    """Wraps Microsoft Presidio with chatstrata-specific recognizers."""

    name: str = "presidio"

    def __init__(
        self,
        score_threshold: float = 0.35,
        hash_salt: str | None = None,
        languages: list[str] | None = None,
        deny_entity_types: frozenset[str] | None = DEFAULT_DENY_ENTITY_TYPES,
    ) -> None:
        self._score_threshold = score_threshold
        self._hash_salt = hash_salt or secrets.token_hex(32)
        self._languages = languages or ["en"]
        self._deny_entity_types = deny_entity_types or frozenset()

        _disable_tldextract_cache()

        registry = RecognizerRegistry()
        registry.load_predefined_recognizers()
        for recognizer in get_all_recognizers():
            registry.add_recognizer(recognizer)

        nlp_engine = self._load_nlp_engine()
        if nlp_engine is not None:
            self._analyzer = AnalyzerEngine(registry=registry, nlp_engine=nlp_engine)
        else:
            self._analyzer = AnalyzerEngine(registry=registry)

    @staticmethod
    def _load_nlp_engine():
        """Try to load a spaCy NLP engine, falling back gracefully."""
        try:
            from presidio_analyzer.nlp_engine import SpacyNlpEngine

            for model in ("en_core_web_lg", "en_core_web_sm"):
                try:
                    return SpacyNlpEngine(models=[{"lang_code": "en", "model_name": model}])
                except OSError:
                    continue
            print(
                "Warning: No spaCy English model found. "
                "NLP-based recognizers (names, locations) will be unavailable. "
                "Install with: python -m spacy download en_core_web_lg",
                file=sys.stderr,
            )
        except ImportError:
            pass
        return None

    def detect(self, text: str) -> list[Entity]:
        """Return entities found in text without modifying it."""
        if not text:
            return []
        results = self._analyzer.analyze(
            text=text,
            language=self._languages[0],
            score_threshold=self._score_threshold,
        )
        entities = [
            Entity(
                type=r.entity_type,
                start=r.start,
                end=r.end,
                text=text[r.start : r.end],
                confidence=r.score,
                recognizer=r.recognition_metadata.get("recognizer_name", ""),
            )
            for r in results
            if r.entity_type not in self._deny_entity_types
        ]
        return sorted(entities, key=lambda e: e.start)

    def redact(
        self, text: str, mode: RedactionMode = RedactionMode.MASK
    ) -> RedactionResult:
        """Apply redaction and return both redacted text and a reversal mapping."""
        entities = self.detect(text)

        if mode == RedactionMode.DETECT_ONLY:
            return RedactionResult(
                original_text=text, redacted_text=text, entities=entities
            )

        if not entities:
            return RedactionResult(original_text=text, redacted_text=text)

        resolved = self._resolve_conflicts(entities)
        redacted, mapping = self._apply(text, resolved, mode)

        return RedactionResult(
            original_text=text,
            redacted_text=redacted,
            entities=entities,
            mapping=mapping,
        )

    @staticmethod
    def _resolve_conflicts(entities: list[Entity]) -> list[Entity]:
        """Keep the highest-confidence entity for each text span.

        Prefers longer spans, then higher confidence when spans overlap.
        """
        sorted_ents = sorted(
            entities, key=lambda e: (-(e.end - e.start), -e.confidence, e.start)
        )
        kept: list[Entity] = []
        for ent in sorted_ents:
            if not any(ent.start < k.end and ent.end > k.start for k in kept):
                kept.append(ent)
        return sorted(kept, key=lambda e: e.start)

    def _apply(
        self, text: str, entities: list[Entity], mode: RedactionMode
    ) -> tuple[str, dict[str, str]]:
        """Apply replacements right-to-left and build the mapping dict."""
        mapping: dict[str, str] = {}
        counters: dict[str, int] = defaultdict(int)

        # Assign placeholders left-to-right for consistent numbering
        replacements: list[tuple[int, int, str]] = []
        for e in entities:
            counters[e.type] += 1
            if mode == RedactionMode.TAG:
                placeholder = f"<PII:{e.type}>{e.text}</PII:{e.type}>"
                mapping[placeholder] = e.text
            elif mode == RedactionMode.MASK:
                placeholder = f"[{e.type}_{counters[e.type]}]"
                mapping[placeholder] = e.text
            elif mode == RedactionMode.REMOVE:
                placeholder = ""
            elif mode == RedactionMode.HASH:
                raw = (e.text + self._hash_salt).encode()
                placeholder = hashlib.sha256(raw).hexdigest()
                mapping[placeholder] = e.text
            else:
                placeholder = "[REDACTED]"
            replacements.append((e.start, e.end, placeholder))

        # Apply right-to-left to preserve offsets
        result = text
        for start, end, placeholder in reversed(replacements):
            result = result[:start] + placeholder + result[end:]

        return result, mapping
