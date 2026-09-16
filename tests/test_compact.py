"""Tests for database compaction and safe replacement."""

from __future__ import annotations

from click.testing import CliRunner

from chatstrata.cli import cli
from chatstrata.core.compact import CORE_TABLES, compact_database
from chatstrata.core.db import connect, rebuild_fts_index
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.models import (
    BlockType,
    ContentBlock,
    ParsedConversation,
    ParsedMessage,
    Role,
)


def _populate_database(path):
    conn = connect(path)
    ensure_source(conn, "test", "Test Source")
    ingest_conversation(
        conn,
        "test",
        ParsedConversation(
            source_native_id="conversation-1",
            title="Compaction fixture",
            messages=[
                ParsedMessage(
                    source_native_id="message-1",
                    role=Role.USER,
                    blocks=[
                        ContentBlock(
                            type=BlockType.TEXT,
                            text="unique compaction search phrase",
                        )
                    ],
                )
            ],
            raw_events=[{"type": "message", "text": "fixture"}],
        ),
    )
    rebuild_fts_index(conn)
    counts = {
        table: conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
        for table in CORE_TABLES
    }
    conn.close()
    return counts


def test_compact_database_preserves_rows_and_retains_backup(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    expected_counts = _populate_database(db_path)

    result = compact_database(db_path)

    assert result.database_path == db_path
    assert result.backup_path is not None
    assert result.backup_path.exists()
    assert result.table_counts == expected_counts

    conn = connect(db_path)
    try:
        actual_counts = {
            table: conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
            for table in CORE_TABLES
        }
        assert actual_counts == expected_counts
        assert (
            conn.execute(
                """
                SELECT COUNT(*)
                FROM duckdb_schemas()
                WHERE schema_name = 'fts_main_content_blocks'
                """
            ).fetchone()[0]
            == 1
        )
    finally:
        conn.close()


def test_compacted_fts_resolves_in_fresh_connection(tmp_path):
    """Compaction builds the FTS index on a copy attached as "compacted_db";
    the stored macro must still resolve when the file is reopened under its
    own name."""
    db_path = tmp_path / "archive.duckdb"
    _populate_database(db_path)

    compact_database(db_path)

    conn = connect(db_path)
    try:
        block_id = conn.execute(
            "SELECT id FROM content_blocks LIMIT 1"
        ).fetchone()[0]
        score = conn.execute(
            "SELECT fts_main_content_blocks.match_bm25(?, 'compaction')",
            [block_id],
        ).fetchone()[0]
        assert score is not None
    finally:
        conn.close()


def test_compact_cli_can_remove_verified_backup(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    expected_counts = _populate_database(db_path)
    runner = CliRunner()

    result = runner.invoke(
        cli,
        ["compact", "--db", str(db_path), "--no-backup", "--yes"],
    )

    assert result.exit_code == 0, result.output
    assert "Compacted size:" in result.output
    assert "Disk space reclaimed:" in result.output
    assert list(tmp_path.glob("archive.duckdb.backup-*")) == []

    conn = connect(db_path)
    try:
        actual_counts = {
            table: conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
            for table in CORE_TABLES
        }
        assert actual_counts == expected_counts
    finally:
        conn.close()
