"""Tests for incremental ingest: mtime tracking, skip, and re-ingest paths."""

from __future__ import annotations

import os
import shutil
from pathlib import Path

import pytest
from click.testing import CliRunner

from chatstrata.cli import _get_file_mtime, cli
from chatstrata.core.db import connect
from chatstrata.core.ingest import ensure_source, get_stored_mtime, ingest_conversation
from chatstrata.core.models import ConversationHandle
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
def sample_copy(tmp_path):
    """Copy the sample fixture into a writable temp dir so we can modify its mtime."""
    src = FIXTURES / "sample_session.jsonl"
    project_dir = tmp_path / "projects" / "-Users-example-myproj"
    project_dir.mkdir(parents=True)
    dst = project_dir / "sample_session.jsonl"
    shutil.copy2(src, dst)
    return dst


# -- Unit-level tests --


class TestMtimeStorage:
    def test_ingest_stores_file_mtime(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
            metadata={"project": "/Users/example/myproj"},
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)
        mtime = os.path.getmtime(FIXTURES / "sample_session.jsonl")

        ingest_conversation(db, adapter.name, conv, source_file_mtime=mtime)

        stored = get_stored_mtime(db, adapter.name, "sample_session")
        assert stored == mtime

    def test_reingest_updates_mtime(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
            metadata={"project": "/Users/example/myproj"},
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)

        ingest_conversation(db, adapter.name, conv, source_file_mtime=1000.0)
        assert get_stored_mtime(db, adapter.name, "sample_session") == 1000.0

        ingest_conversation(db, adapter.name, conv, source_file_mtime=2000.0)
        assert get_stored_mtime(db, adapter.name, "sample_session") == 2000.0

    def test_reingest_removes_stale_embeddings_before_replacing_messages(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
            metadata={"project": "/Users/example/myproj"},
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)

        ingest_conversation(db, adapter.name, conv, source_file_mtime=1000.0)
        message_id = db.execute(
            """
            SELECT id FROM messages
            WHERE conversation_id = (
                SELECT id FROM conversations
                WHERE source_id = ? AND source_native_id = ?
            )
            LIMIT 1
            """,
            [adapter.name, "sample_session"],
        ).fetchone()[0]
        db.execute(
            "INSERT INTO message_embeddings (message_id, model, vector) VALUES (?, ?, ?)",
            [message_id, "test-model", [0.1, 0.2]],
        )

        ingest_conversation(db, adapter.name, conv, source_file_mtime=2000.0)

        stale_embedding_count = db.execute(
            "SELECT COUNT(*) FROM message_embeddings WHERE message_id = ?",
            [message_id],
        ).fetchone()[0]
        assert stale_embedding_count == 0

    def test_get_stored_mtime_returns_none_for_missing(self, db):
        assert get_stored_mtime(db, "nonexistent", "nonexistent") is None


class TestGetFileMtime:
    def test_returns_mtime_for_valid_path(self):
        handle = ConversationHandle(
            source_native_id="test",
            path=FIXTURES / "sample_session.jsonl",
        )
        mtime = _get_file_mtime(handle)
        assert mtime is not None
        assert isinstance(mtime, float)

    def test_returns_none_for_no_path(self):
        handle = ConversationHandle(source_native_id="test", path=None)
        assert _get_file_mtime(handle) is None

    def test_returns_none_for_missing_file(self, tmp_path):
        handle = ConversationHandle(
            source_native_id="test",
            path=tmp_path / "does_not_exist.jsonl",
        )
        assert _get_file_mtime(handle) is None


class TestIncrementalSkipLogic:
    def test_skip_when_mtime_unchanged(self, db, sample_copy):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id=sample_copy.stem,
            path=sample_copy,
            metadata={"project": "/Users/example/myproj"},
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)
        file_mtime = os.path.getmtime(sample_copy)
        ingest_conversation(db, adapter.name, conv, source_file_mtime=file_mtime)

        stored = get_stored_mtime(db, adapter.name, sample_copy.stem)
        current = os.path.getmtime(sample_copy)
        assert stored == current

    def test_reingest_when_mtime_changes(self, db, sample_copy):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id=sample_copy.stem,
            path=sample_copy,
            metadata={"project": "/Users/example/myproj"},
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)
        file_mtime = os.path.getmtime(sample_copy)
        ingest_conversation(db, adapter.name, conv, source_file_mtime=file_mtime)

        original_mtime = os.path.getmtime(sample_copy)
        new_mtime = original_mtime + 100
        os.utime(sample_copy, (new_mtime, new_mtime))

        stored = get_stored_mtime(db, adapter.name, sample_copy.stem)
        current = os.path.getmtime(sample_copy)
        assert stored != current


# -- CLI integration tests --


class TestIncrementalCLI:
    def test_first_ingest_then_incremental_skips(self, tmp_path, sample_copy):
        db_path = str(tmp_path / "cli_test.duckdb")
        projects_dir = str(sample_copy.parent.parent)
        runner = CliRunner()

        result1 = runner.invoke(
            cli, ["ingest", "claude_code", "--db", db_path, "--path", projects_dir],
        )
        assert result1.exit_code == 0
        assert "Ingested: 1" in result1.output
        assert "Skipped: 0" in result1.output

        result2 = runner.invoke(
            cli, ["ingest", "claude_code", "--incremental", "--db", db_path, "--path", projects_dir],
        )
        assert result2.exit_code == 0
        assert "Skipped: 1" in result2.output
        assert "Ingested: 0" in result2.output

    def test_incremental_reingests_after_file_change(self, tmp_path, sample_copy):
        db_path = str(tmp_path / "cli_test.duckdb")
        projects_dir = str(sample_copy.parent.parent)
        runner = CliRunner()

        runner.invoke(
            cli, ["ingest", "claude_code", "--db", db_path, "--path", projects_dir],
        )

        new_mtime = os.path.getmtime(sample_copy) + 100
        os.utime(sample_copy, (new_mtime, new_mtime))

        result = runner.invoke(
            cli, ["ingest", "claude_code", "--incremental", "--db", db_path, "--path", projects_dir],
        )
        assert result.exit_code == 0
        assert "Ingested: 1" in result.output
        assert "Skipped: 0" in result.output

    def test_skipped_count_appears_without_incremental(self, tmp_path, sample_copy):
        db_path = str(tmp_path / "cli_test.duckdb")
        projects_dir = str(sample_copy.parent.parent)
        runner = CliRunner()

        result = runner.invoke(
            cli, ["ingest", "claude_code", "--db", db_path, "--path", projects_dir],
        )
        assert result.exit_code == 0
        assert "Skipped: 0" in result.output
