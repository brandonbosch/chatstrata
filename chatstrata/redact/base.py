"""Redaction engine protocol and shared types.

The engine is pluggable: chatstrata ships with a Presidio-backed default in
`chatstrata.redact.presidio_engine` (TODO), but contributors can implement
alternative engines (DataFog, regex-only, fine-tuned local models) by
implementing this protocol.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Protocol, runtime_checkable


class RedactionMode(str, Enum):
    DETECT_ONLY = "detect_only"   # find entities, do not modify text
    TAG = "tag"                   # wrap entities in <PII:type>...</PII:type>
    MASK = "mask"                 # replace with [TYPE_N]
    REMOVE = "remove"             # delete entity text entirely
    HASH = "hash"                 # replace with stable hash of the entity


@dataclass
class Entity:
    type: str            # EMAIL, PHONE, API_KEY, FILE_PATH, etc.
    start: int           # character offset in original text
    end: int             # character offset, exclusive
    text: str            # the actual matched text
    confidence: float = 1.0
    recognizer: str = ""


@dataclass
class RedactionResult:
    original_text: str
    redacted_text: str
    entities: list[Entity] = field(default_factory=list)
    mapping: dict[str, str] = field(default_factory=dict)  # placeholder -> original


@runtime_checkable
class RedactionEngine(Protocol):
    """A pluggable redaction engine."""

    name: str

    def detect(self, text: str) -> list[Entity]:
        """Return entities found in text without modifying it."""
        ...

    def redact(self, text: str, mode: RedactionMode = RedactionMode.MASK) -> RedactionResult:
        """Apply redaction. Returns both redacted text and a mapping for reversal."""
        ...


# TODO: presidio_engine.py — wraps Microsoft Presidio with chatstrata-specific
# recognizers for API keys, file paths, internal hostnames, git remotes, etc.
