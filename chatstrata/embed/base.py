"""Embedding provider protocol and shared types.

The provider is pluggable: chatstrata ships with a sentence-transformers
default in `chatstrata.embed.local_provider`, but alternative providers
(OpenAI, Voyage, Cohere) can implement this protocol.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class EmbeddingProvider(Protocol):
    """A pluggable embedding provider."""

    name: str
    dimension: int

    def embed_texts(self, texts: list[str]) -> list[list[float]]:
        """Embed a batch of texts. Returns one vector per input text."""
        ...

    def embed_query(self, query: str) -> list[float]:
        """Embed a single query string for similarity search."""
        ...
