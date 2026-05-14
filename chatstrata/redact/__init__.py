"""PII redaction.

Install the optional `redact` extras to get the runtime dependencies:
    uv pip install "chatstrata[redact]"
"""

from chatstrata.redact.base import Entity, RedactionEngine, RedactionMode, RedactionResult

__all__ = ["Entity", "RedactionEngine", "RedactionMode", "RedactionResult"]


def get_engine(**kwargs) -> RedactionEngine:
    """Factory function to get the default (Presidio) engine.

    Raises ImportError if [redact] extras are not installed.
    """
    from chatstrata.redact.presidio_engine import PresidioEngine

    return PresidioEngine(**kwargs)

