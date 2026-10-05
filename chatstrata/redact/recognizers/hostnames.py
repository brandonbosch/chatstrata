"""Recognizers for internal/private hostnames that reveal infrastructure."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_hostname_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for internal hostnames and IP addresses."""
    return [
        PatternRecognizer(
            supported_entity="INTERNAL_HOSTNAME",
            name="InternalHostnameRecognizer",
            patterns=[
                Pattern(
                    "internal_domain",
                    r"(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+(?:internal|local|corp|lan|intranet|private)(?:\.[a-zA-Z]{2,})?",
                    0.8,
                ),
                Pattern(
                    "k8s_service_dns",
                    r"[a-z0-9-]+\.[a-z0-9-]+\.svc\.cluster\.local",
                    0.85,
                ),
            ],
        ),
        PatternRecognizer(
            supported_entity="PRIVATE_IP",
            name="PrivateIpRecognizer",
            patterns=[
                Pattern("rfc1918_10", r"10\.\d{1,3}\.\d{1,3}\.\d{1,3}", 0.75),
                Pattern(
                    "rfc1918_172",
                    r"172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}",
                    0.75,
                ),
                Pattern("rfc1918_192", r"192\.168\.\d{1,3}\.\d{1,3}", 0.75),
            ],
        ),
    ]
