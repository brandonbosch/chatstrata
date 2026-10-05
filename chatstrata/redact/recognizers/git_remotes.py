"""Recognizers for git remote URLs containing credentials."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_git_remote_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for git remote URLs that embed credentials."""
    return [
        PatternRecognizer(
            supported_entity="GIT_CREDENTIAL_URL",
            name="GitCredentialUrlRecognizer",
            patterns=[
                Pattern(
                    "git_https_cred",
                    r"https?://[A-Za-z0-9._~%-]+:[A-Za-z0-9._~%!$&'()*+,;=:@/-]+@[A-Za-z0-9.-]+(?:/[^\s'\")\]}>]*)?",
                    0.9,
                ),
                Pattern(
                    "git_ssh_url",
                    r"git@[A-Za-z0-9.-]+:[A-Za-z0-9._-]+/[A-Za-z0-9._-]+(?:\.git)?",
                    0.7,
                ),
            ],
        ),
    ]
