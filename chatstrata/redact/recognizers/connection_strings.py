"""Recognizers for database connection strings."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_connection_string_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for common database connection URL formats."""
    return [
        PatternRecognizer(
            supported_entity="CONNECTION_STRING",
            name="ConnectionStringRecognizer",
            patterns=[
                Pattern("postgres_url", r"postgres(?:ql)?://[^\s'\")\]}>]+", 0.9),
                Pattern("mysql_url", r"mysql://[^\s'\")\]}>]+", 0.9),
                Pattern("mongodb_url", r"mongodb(?:\+srv)?://[^\s'\")\]}>]+", 0.9),
            ],
        ),
    ]
