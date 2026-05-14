"""PII redaction (stub for v0).

Full implementation is a separate work item. The protocol is defined here so
contributors can start implementing engines (Presidio is the planned default)
without waiting for the rest of the system.

Install the optional `redact` extras to get the runtime dependencies:
    uv pip install "chatstrata[redact]"
"""

from chatstrata.redact.base import RedactionEngine, RedactionMode, RedactionResult

__all__ = ["RedactionEngine", "RedactionMode", "RedactionResult"]
