"""Adapter protocol and entry-point registry.

Each source (Claude Code, claude.ai export, ChatGPT export, etc.) implements
the SourceAdapter protocol. Adapters are discovered via the
`chatstrata.sources` entry point group, so third-party packages can register
their own without modifying chatstrata's code.
"""

from __future__ import annotations

from collections.abc import Iterable
from importlib.metadata import entry_points
from typing import Protocol, runtime_checkable

from chatstrata.core.models import ConversationHandle, ParsedConversation


@runtime_checkable
class SourceAdapter(Protocol):
    """Contract every source adapter must satisfy.

    Implementations should be classes with a no-arg constructor.
    """

    name: str
    """Stable identifier, lowercase, underscores. e.g. "claude_code"."""

    display_name: str
    """Human-readable name. e.g. "Claude Code"."""

    version: str
    """Adapter version. Bump when parse semantics change."""

    schema_version: int
    """Which chatstrata schema version this adapter targets."""

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        """Find all conversations available from this source.

        `config` is an optional dict of source-specific knobs (paths, etc.).
        Yield ConversationHandle objects; the ingester will call `parse()` on
        each one it wants to ingest.
        """
        ...

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        """Parse one conversation referenced by a handle into canonical form."""
        ...


def load_adapters() -> dict[str, SourceAdapter]:
    """Load all registered adapters from entry points.

    Returns a dict of {adapter_name: instance}.
    """
    adapters: dict[str, SourceAdapter] = {}
    eps = entry_points(group="chatstrata.sources")
    for ep in eps:
        cls = ep.load()
        instance = cls()
        if not isinstance(instance, SourceAdapter):
            raise TypeError(
                f"Entry point '{ep.name}' loaded a {type(instance).__name__} "
                f"that does not satisfy the SourceAdapter protocol."
            )
        adapters[instance.name] = instance
    return adapters
