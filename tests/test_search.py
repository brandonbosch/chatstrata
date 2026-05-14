"""Tests for full-text search functionality."""

from datetime import datetime, timezone
from pathlib import Path

import pytest

from chatstrata.core.db import connect, rebuild_fts_index
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.models import ConversationHandle
from chatstrata.core.search import SearchResult, search_messages, snippet
from chatstrata.sources.claude_code.adapter import ClaudeCodeAdapter

FIXTURES = (
    Path(__file__).parent.parent
    / "chatstrata"
    / "sources"
    / "claude_code"
    / "tests"
    / "fixtures"
)


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


class TestSearchMessages:
    def test_empty_db_returns_empty(self, db):
        results = search_messages(db, "anything")
        assert results == []

    def test_basic_search(self, populated_db):
        results = search_messages(populated_db, "auth")
        assert len(results) > 0
        assert all(isinstance(r, SearchResult) for r in results)

    def test_results_contain_expected_fields(self, populated_db):
        results = search_messages(populated_db, "refactor")
        assert len(results) > 0
        r = results[0]
        assert r.conversation_title == "Refactor the user auth module"
        assert r.source_id == "claude_code"
        assert r.message_role in ("user", "assistant")
        assert r.text is not None

    def test_results_ordered_by_score(self, populated_db):
        results = search_messages(populated_db, "auth")
        if len(results) > 1:
            scores = [r.score for r in results]
            assert scores == sorted(scores, reverse=True)

    def test_no_match_returns_empty(self, populated_db):
        results = search_messages(populated_db, "xyzzy_not_a_real_word")
        assert results == []

    def test_source_filter(self, populated_db):
        results = search_messages(populated_db, "auth", source="claude_code")
        assert len(results) > 0
        assert all(r.source_id == "claude_code" for r in results)

        results = search_messages(populated_db, "auth", source="nonexistent")
        assert results == []

    def test_limit(self, populated_db):
        results = search_messages(populated_db, "auth", limit=1)
        assert len(results) <= 1

    def test_since_filter(self, populated_db):
        future = datetime(2030, 1, 1, tzinfo=timezone.utc)
        results = search_messages(populated_db, "auth", since=future)
        assert results == []

    def test_until_filter(self, populated_db):
        past = datetime(2020, 1, 1, tzinfo=timezone.utc)
        results = search_messages(populated_db, "auth", until=past)
        assert results == []


class TestSnippet:
    def test_snippet_with_match(self):
        text = "The quick brown fox jumps over the lazy dog"
        s = snippet(text, "fox")
        assert "fox" in s

    def test_snippet_no_match_shows_beginning(self):
        text = "A" * 500
        s = snippet(text, "xyzzy")
        assert s.startswith("A")
        assert s.endswith("...")

    def test_snippet_empty_text(self):
        assert snippet(None, "test") == ""
        assert snippet("", "test") == ""

    def test_snippet_ellipsis_for_long_text(self):
        text = "prefix " * 50 + "KEYWORD" + " suffix" * 50
        s = snippet(text, "KEYWORD", context_chars=20)
        assert "..." in s
        assert "KEYWORD" in s


class TestReindex:
    def test_reindex_is_idempotent(self, populated_db):
        rebuild_fts_index(populated_db)
        rebuild_fts_index(populated_db)
        results = search_messages(populated_db, "auth")
        assert len(results) > 0

    def test_reindex_picks_up_new_data(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)
        ingest_conversation(db, adapter.name, conv)
        rebuild_fts_index(db)
        results = search_messages(db, "auth")
        assert len(results) > 0
