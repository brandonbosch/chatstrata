"""Recognizers for JWT and bearer tokens."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_token_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for JWT and bearer token formats."""
    return [
        PatternRecognizer(
            supported_entity="JWT_TOKEN",
            name="JwtTokenRecognizer",
            patterns=[
                Pattern(
                    "jwt",
                    r"eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}",
                    0.95,
                ),
            ],
        ),
        PatternRecognizer(
            supported_entity="BEARER_TOKEN",
            name="BearerTokenRecognizer",
            patterns=[
                Pattern("bearer", r"Bearer\s+[A-Za-z0-9_.=-]{20,}", 0.8),
            ],
        ),
    ]
