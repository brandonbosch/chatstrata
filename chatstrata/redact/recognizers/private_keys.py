"""Recognizers for PEM-encoded private keys."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_private_key_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for PEM-encoded private key blocks."""
    return [
        PatternRecognizer(
            supported_entity="PRIVATE_KEY",
            name="PemPrivateKeyRecognizer",
            patterns=[
                Pattern(
                    "pem_private_key",
                    r"-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----",
                    0.95,
                ),
            ],
        ),
    ]
