"""Recognizers for file paths that reveal usernames."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_path_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for home-directory file paths."""
    return [
        PatternRecognizer(
            supported_entity="FILE_PATH",
            name="UnixHomePathRecognizer",
            patterns=[
                Pattern(
                    "unix_home_path",
                    r"/(?:Users|home)/[A-Za-z0-9._-]+/[^\s'\")\]}>]*",
                    0.85,
                ),
            ],
        ),
        PatternRecognizer(
            supported_entity="FILE_PATH",
            name="WindowsHomePathRecognizer",
            patterns=[
                Pattern(
                    "windows_home_path",
                    r"[A-Z]:\\Users\\[A-Za-z0-9._-]+\\[^\s'\")\]}>]*",
                    0.85,
                ),
            ],
        ),
    ]
