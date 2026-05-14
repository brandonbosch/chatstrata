"""Embeddings and semantic search.

Install the optional ``embeddings`` extras to get the runtime dependencies::

    uv pip install "chatstrata[embeddings]"
"""

from chatstrata.embed.base import EmbeddingProvider

__all__ = ["EmbeddingProvider"]


def get_provider(model_name: str = "all-MiniLM-L6-v2", **kwargs) -> EmbeddingProvider:
    """Factory function to get the default (sentence-transformers) provider.

    Raises ImportError if [embeddings] extras are not installed.
    """
    from chatstrata.embed.local_provider import SentenceTransformerProvider

    return SentenceTransformerProvider(model_name=model_name, **kwargs)
