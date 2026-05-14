"""Local sentence-transformers embedding provider."""

from __future__ import annotations


class SentenceTransformerProvider:
    """Embedding provider using sentence-transformers models.

    Default model: all-MiniLM-L6-v2 (384 dimensions, ~23M params, fast).
    """

    def __init__(self, model_name: str = "all-MiniLM-L6-v2"):
        try:
            from sentence_transformers import SentenceTransformer
        except ImportError:
            raise ImportError(
                "sentence-transformers is required for local embeddings.\n"
                'Install with: uv pip install "chatstrata[embeddings]"'
            ) from None
        self._model = SentenceTransformer(model_name)
        self.name = f"sentence-transformers/{model_name}"
        self.dimension = self._model.get_sentence_embedding_dimension()

    def embed_texts(self, texts: list[str]) -> list[list[float]]:
        embeddings = self._model.encode(texts, normalize_embeddings=True)
        return embeddings.tolist()

    def embed_query(self, query: str) -> list[float]:
        embedding = self._model.encode(query, normalize_embeddings=True)
        return embedding.tolist()
