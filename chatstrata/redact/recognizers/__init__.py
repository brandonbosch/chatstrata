"""Custom Presidio recognizers for developer-specific PII."""

from __future__ import annotations

from presidio_analyzer import PatternRecognizer

from chatstrata.redact.recognizers.api_keys import get_api_key_recognizers
from chatstrata.redact.recognizers.connection_strings import get_connection_string_recognizers
from chatstrata.redact.recognizers.paths import get_path_recognizers
from chatstrata.redact.recognizers.tokens import get_token_recognizers


def get_all_recognizers() -> list[PatternRecognizer]:
    """Return all custom recognizers for registration with Presidio."""
    return (
        get_api_key_recognizers()
        + get_path_recognizers()
        + get_connection_string_recognizers()
        + get_token_recognizers()
    )
