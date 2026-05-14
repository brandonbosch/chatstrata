"""Tests for embedding generation and semantic search."""

from __future__ import annotations

import math
from datetime import datetime, timezone
from pathlib import Path

import pytest

from chatstrata.core.db import connect, rebuild_fts_index
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.models import ConversationHandle
from chatstrata.embed.base import EmbeddingProvider
from chatstrata.embed.search import hybrid_search, semantic_search
from chatstrata.sources.claude_code.adapter import ClaudeCodeAdapter

FIXTURES = (
    Path(__file__).parent.parent
    / "chatstrata"
    / "sources"
    / "claude_code"
    / "tests"
    / "fixtures"
)


class MockProvider:
    """Deterministic embedding provider for testing."""

    name = "mock/test-model"
    dimension = 4

    def embed_texts(self, texts: list[str]) -> list[list[float]]:
        return [self._embed(t) for t in texts]

    def embed_query(self, query: str) -> list[float]:
        return self._embed(query)

    def _embed(self, text: str) -> list[float]:
        words = text.lower().split()
        v = [
            1.0 if "auth" in words or "login" in words else 0.0,
            1.0 if "refactor" in words else 0.0,
            1.0 if "help" in words or "module" in words else 0.0,
            0.5,
        ]
        norm = math.sqrt(sum(x * x for x in v)) or 1.0
        return [x / norm for x in v]


@pytest.fixture
def provider():
    return MockProvider()


@pytest.fixture
def db(tmp_path):
    conn = connect(tmp_path / "test.duckdb")
    yield conn
    conn.close()


@pytest.fixture
def populated_db(db):
    adapter = ClaudeCodeAdapter()
    handle = ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
        metadata={"project": "/Users/example/myproj"},
    )
    ensure_source(db, adapter.name, adapter.display_name, adapter.version)
    conv = adapter.parse(handle)
    ingest_conversation(db, adapter.name, conv)
    rebuild_fts_index(db)
    return db


def _get_message_ids(conn) -> list[str]:
    rows = conn.execute("SELECT id FROM messages").fetchall()
    return [r[0] for r in rows]


def _get_message_texts(conn) -> list[tuple[str, str]]:
    rows = conn.execute(
        """
        SELECT m.id, STRING_AGG(cb.text, ' ' ORDER BY cb.block_index)
        FROM messages m
        JOIN content_blocks cb ON cb.message_id = m.id AND cb.text IS NOT NULL
        GROUP BY m.id
        """
    ).fetchall()
    return [(r[0], r[1]) for r in rows if r[1]]


def _embed_all_messages(conn, provider: MockProvider) -> int:
    messages = _get_message_texts(conn)
    texts = [text for _, text in messages]
    if not texts:
        return 0
    vectors = provider.embed_texts(texts)
    for (msg_id, _), vector in zip(messages, vectors):
        conn.execute(
            "INSERT INTO message_embeddings (message_id, model, vector) VALUES (?, ?, ?)",
            [msg_id, provider.name, vector],
        )
    return len(messages)


class TestEmbeddingProvider:
    def test_mock_provider_is_protocol_compliant(self, provider):
        assert isinstance(provider, EmbeddingProvider)

    def test_embed_texts_returns_correct_count(self, provider):
        texts = ["hello world", "another text"]
        result = provider.embed_texts(texts)
        assert len(result) == 2
        assert all(len(v) == provider.dimension for v in result)

    def test_embed_query_returns_vector(self, provider):
        result = provider.embed_query("test query")
        assert len(result) == provider.dimension

    def test_vectors_are_normalized(self, provider):
        v = provider.embed_query("auth login")
        norm = math.sqrt(sum(x * x for x in v))
        assert abs(norm - 1.0) < 1e-6


class TestEmbedGeneration:
    def test_embed_populates_table(self, populated_db, provider):
        count = _embed_all_messages(populated_db, provider)
        assert count > 0

        stored = populated_db.execute(
            "SELECT COUNT(*) FROM message_embeddings WHERE model = ?",
            [provider.name],
        ).fetchone()[0]
        assert stored == count

    def test_skip_already_embedded(self, populated_db, provider):
        count1 = _embed_all_messages(populated_db, provider)
        assert count1 > 0

        already = populated_db.execute(
            "SELECT COUNT(*) FROM message_embeddings WHERE model = ?",
            [provider.name],
        ).fetchone()[0]
        assert already == count1

        messages = _get_message_texts(populated_db)
        for msg_id, text in messages:
            existing = populated_db.execute(
                "SELECT 1 FROM message_embeddings WHERE message_id = ? AND model = ?",
                [msg_id, provider.name],
            ).fetchone()
            assert existing is not None

    def test_different_model_embeds_separately(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)

        other = MockProvider()
        other.name = "mock/other-model"
        _embed_all_messages(populated_db, other)

        count = populated_db.execute(
            "SELECT COUNT(DISTINCT model) FROM message_embeddings"
        ).fetchone()[0]
        assert count == 2


class TestSemanticSearch:
    def test_empty_db_returns_empty(self, db, provider):
        results = semantic_search(db, provider.embed_query("test"), provider.name)
        assert results == []

    def test_basic_semantic_search(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth refactor")
        results = semantic_search(populated_db, query_vec, provider.name)
        assert len(results) > 0

    def test_results_ordered_by_score(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth module")
        results = semantic_search(populated_db, query_vec, provider.name)
        if len(results) > 1:
            scores = [r.score for r in results]
            assert scores == sorted(scores, reverse=True)

    def test_source_filter(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth")
        results = semantic_search(
            populated_db, query_vec, provider.name, source="claude_code"
        )
        assert len(results) > 0
        assert all(r.source_id == "claude_code" for r in results)

        results = semantic_search(
            populated_db, query_vec, provider.name, source="nonexistent"
        )
        assert results == []

    def test_limit(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth")
        results = semantic_search(populated_db, query_vec, provider.name, limit=1)
        assert len(results) <= 1

    def test_since_filter(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        future = datetime(2030, 1, 1, tzinfo=timezone.utc)
        query_vec = provider.embed_query("auth")
        results = semantic_search(
            populated_db, query_vec, provider.name, since=future
        )
        assert results == []

    def test_wrong_model_returns_empty(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth")
        results = semantic_search(populated_db, query_vec, "nonexistent/model")
        assert results == []

    def test_result_has_expected_fields(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth refactor")
        results = semantic_search(populated_db, query_vec, provider.name)
        assert len(results) > 0
        r = results[0]
        assert r.source_id == "claude_code"
        assert r.message_role in ("user", "assistant")
        assert r.text is not None


class TestHybridSearch:
    def test_hybrid_combines_results(self, populated_db, provider):
        _embed_all_messages(populated_db, provider)
        query_vec = provider.embed_query("auth")
        results = hybrid_search(
            populated_db, "auth", query_vec, provider.name
        )
        assert len(results) > 0

    def test_hybrid_with_no_embeddings_falls_back(self, populated_db, provider):
        query_vec = provider.embed_query("auth")
        results = hybrid_search(
            populated_db, "auth", query_vec, provider.name
        )
        assert len(results) > 0
