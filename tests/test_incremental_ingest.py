"""Tests for incremental ingest: mtime tracking, skip, and re-ingest paths."""

from __future__ import annotations

import os
import shutil
from pathlib import Path

import pytest
from click.testing import CliRunner

from chatstrata.cli import _get_file_mtime, cli
from chatstrata.core.db import connect
from chatstrata.core.ingest import (
    IngestAction,
    ensure_source,
    get_stored_mtime,
    ingest_conversation,
    ingest_conversation_with_status,
)
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

    def test_noop_reingest_preserves_message_ids_and_embeddings(self, db):
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

        outcome = ingest_conversation_with_status(
            db,
            adapter.name,
            conv,
            source_file_mtime=2000.0,
        )

        embedding_count = db.execute(
            "SELECT COUNT(*) FROM message_embeddings WHERE message_id = ?",
            [message_id],
        ).fetchone()[0]
        assert outcome.action == IngestAction.UNCHANGED
        assert embedding_count == 1
        assert get_stored_mtime(db, adapter.name, "sample_session") == 2000.0

    def test_changed_content_replaces_messages_and_removes_stale_embeddings(self, db):
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
            "SELECT id FROM messages ORDER BY sequence_index LIMIT 1"
        ).fetchone()[0]
        db.execute(
            "INSERT INTO message_embeddings (message_id, model, vector) VALUES (?, ?, ?)",
            [message_id, "test-model", [0.1, 0.2]],
        )

        changed = conv.model_copy(deep=True)
        changed.messages[0].blocks[0].text = "Changed content"
        outcome = ingest_conversation_with_status(
            db,
            adapter.name,
            changed,
            source_file_mtime=2000.0,
        )

        assert outcome.action == IngestAction.REPLACED
        assert (
            db.execute(
                "SELECT COUNT(*) FROM message_embeddings WHERE message_id = ?",
                [message_id],
            ).fetchone()[0]
            == 0
        )

    def test_append_only_reingest_preserves_existing_rows(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
            metadata={"project": "/Users/example/myproj"},
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        complete = adapter.parse(handle)
        initial = complete.model_copy(deep=True)
        initial.messages = initial.messages[:2]
        initial.raw_events = initial.raw_events[:3]

        ingest_conversation(db, adapter.name, initial, source_file_mtime=1000.0)
        original_ids = [
            row[0]
            for row in db.execute(
                "SELECT id FROM messages ORDER BY sequence_index"
            ).fetchall()
        ]
        db.execute(
            "INSERT INTO message_embeddings (message_id, model, vector) VALUES (?, ?, ?)",
            [original_ids[0], "test-model", [0.1, 0.2]],
        )

        outcome = ingest_conversation_with_status(
            db,
            adapter.name,
            complete,
            source_file_mtime=2000.0,
        )

        current_ids = [
            row[0]
            for row in db.execute(
                "SELECT id FROM messages ORDER BY sequence_index"
            ).fetchall()
        ]
        assert outcome.action == IngestAction.APPENDED
        assert current_ids[:2] == original_ids
        assert len(current_ids) == len(complete.messages)
        assert db.execute(
            "SELECT message_count FROM conversations"
        ).fetchone()[0] == len(complete.messages)
        assert db.execute("SELECT COUNT(*) FROM raw_events").fetchone()[0] == len(
            complete.raw_events
        )
        assert (
            db.execute(
                "SELECT COUNT(*) FROM message_embeddings WHERE message_id = ?",
                [original_ids[0]],
            ).fetchone()[0]
            == 1
        )

    def test_legacy_content_hash_is_upgraded_without_rewriting(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)
        ingest_conversation(db, adapter.name, conv)
        original_ids = db.execute(
            "SELECT id FROM messages ORDER BY sequence_index"
        ).fetchall()
        db.execute(
            "UPDATE conversations SET content_hash = 'legacy-hash' "
            "WHERE source_native_id = 'sample_session'"
        )

        outcome = ingest_conversation_with_status(db, adapter.name, conv)

        assert outcome.action == IngestAction.UNCHANGED
        assert (
            db.execute("SELECT id FROM messages ORDER BY sequence_index").fetchall()
            == original_ids
        )

    def test_indexed_conversation_field_change_uses_safe_replacement(self, db):
        adapter = ClaudeCodeAdapter()
        handle = ConversationHandle(
            source_native_id="sample_session",
            path=FIXTURES / "sample_session.jsonl",
        )
        ensure_source(db, adapter.name, adapter.display_name, adapter.version)
        conv = adapter.parse(handle)
        ingest_conversation(db, adapter.name, conv)
        original_ids = db.execute(
            "SELECT id FROM messages ORDER BY sequence_index"
        ).fetchall()

        changed = conv.model_copy(deep=True)
        changed.project = "/Users/example/renamed-project"
        outcome = ingest_conversation_with_status(db, adapter.name, changed)

        assert outcome.action == IngestAction.REPLACED
        assert db.execute("SELECT project FROM conversations").fetchone()[0] == (
            "/Users/example/renamed-project"
        )
        assert (
            db.execute("SELECT id FROM messages ORDER BY sequence_index").fetchall()
            != original_ids
        )

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

    def test_incremental_skips_when_only_file_mtime_changes(self, tmp_path, sample_copy):
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
        assert "Ingested: 0" in result.output
        assert "Skipped: 1" in result.output

    def test_full_ingest_skips_unchanged_content(self, tmp_path, sample_copy):
        db_path = str(tmp_path / "cli_test.duckdb")
        projects_dir = str(sample_copy.parent.parent)
        runner = CliRunner()

        runner.invoke(
            cli, ["ingest", "claude_code", "--db", db_path, "--path", projects_dir],
        )
        result = runner.invoke(
            cli, ["ingest", "claude_code", "--db", db_path, "--path", projects_dir],
        )

        assert result.exit_code == 0
        assert "Ingested: 0" in result.output
        assert "Skipped: 1" in result.output

    def test_skipped_count_appears_without_incremental(self, tmp_path, sample_copy):
        db_path = str(tmp_path / "cli_test.duckdb")
        projects_dir = str(sample_copy.parent.parent)
        runner = CliRunner()

        result = runner.invoke(
            cli, ["ingest", "claude_code", "--db", db_path, "--path", projects_dir],
        )
        assert result.exit_code == 0
        assert "Skipped: 0" in result.output
