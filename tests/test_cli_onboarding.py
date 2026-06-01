"""Tests for first-run CLI onboarding commands."""

from __future__ import annotations

import json
from datetime import datetime, timezone

from click.testing import CliRunner

from chatstrata.cli import cli
from chatstrata.core.db import connect, get_schema_version
from chatstrata.core.migrations import LATEST_VERSION
from chatstrata.core.models import (
    BlockType,
    ContentBlock,
    ConversationHandle,
    ParsedConversation,
    ParsedMessage,
    Role,
)


def test_init_creates_database(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    runner = CliRunner()

    result = runner.invoke(cli, ["init", "--db", str(db_path), "--no-discover"])

    assert result.exit_code == 0
    assert db_path.exists()
    assert "Created database" in result.output
    assert f"Schema: version {LATEST_VERSION}, latest {LATEST_VERSION}" in result.output
    assert "chatstrata ingest <source> --incremental" in result.output

    conn = connect(db_path)
    try:
        assert get_schema_version(conn) == LATEST_VERSION
    finally:
        conn.close()


def test_init_reports_existing_database(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    conn = connect(db_path)
    conn.close()
    runner = CliRunner()

    result = runner.invoke(cli, ["init", "--db", str(db_path), "--no-discover"])

    assert result.exit_code == 0
    assert "Using existing database" in result.output


def test_paths_shows_database_override(tmp_path):
    db_path = tmp_path / "custom.duckdb"
    runner = CliRunner()

    result = runner.invoke(cli, ["paths", "--db", str(db_path)])

    assert result.exit_code == 0
    assert f"Database: {db_path}" in result.output
    assert "Data dir:" in result.output
    assert "Config:" in result.output
    assert "CHATSTRATA_DB" in result.output


def test_query_rejects_mutating_sql(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    conn = connect(db_path)
    conn.close()
    runner = CliRunner()

    result = runner.invoke(cli, ["query", "DROP TABLE conversations", "--db", str(db_path)])

    assert result.exit_code != 0
    assert "DROP queries are not allowed" in result.output


def test_query_allows_select(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    conn = connect(db_path)
    conn.close()
    runner = CliRunner()

    result = runner.invoke(cli, ["query", "SELECT COUNT(*) AS conversations FROM conversations", "--db", str(db_path)])

    assert result.exit_code == 0
    assert "conversations" in result.output


def test_mcp_config_claude_code_defaults_to_uvx():
    runner = CliRunner()

    result = runner.invoke(cli, ["mcp", "config", "claude-code"])

    assert result.exit_code == 0
    assert result.output.strip() == (
        "claude mcp add --transport stdio --scope user chatstrata -- "
        "uvx --from 'chatstrata[mcp]' chatstrata-mcp"
    )


def test_mcp_config_claude_desktop_json_with_db(tmp_path):
    db_path = tmp_path / "archive.duckdb"
    runner = CliRunner()

    result = runner.invoke(
        cli,
        ["mcp", "config", "claude-desktop", "--runner", "installed", "--db", str(db_path)],
    )

    assert result.exit_code == 0
    config = json.loads(result.output)
    server = config["mcpServers"]["chatstrata"]
    assert server["type"] == "stdio"
    assert server["command"] == "chatstrata-mcp"
    assert server["args"] == []
    assert server["env"]["CHATSTRATA_DB"] == str(db_path)


class StubAutoAdapter:
    name = "stub_auto"
    display_name = "Stub Auto"
    version = "0.1.0"
    schema_version = 1

    def __init__(self, path):
        self.path = path

    def discover(self, config=None):
        if self.path.exists():
            yield ConversationHandle(source_native_id=self.path.stem, path=self.path)

    def parse(self, handle):
        return ParsedConversation(
            source_native_id=handle.source_native_id,
            title="Auto fixture",
            started_at=datetime(2026, 1, 1, tzinfo=timezone.utc),
            messages=[
                ParsedMessage(
                    source_native_id="m1",
                    role=Role.USER,
                    created_at=datetime(2026, 1, 1, tzinfo=timezone.utc),
                    blocks=[ContentBlock(type=BlockType.TEXT, text="hello from auto ingest")],
                )
            ],
        )


def test_ingest_auto_detects_sources_and_then_uses_incremental(tmp_path, monkeypatch):
    source_file = tmp_path / "session.jsonl"
    source_file.write_text("{}\n", encoding="utf-8")
    adapter = StubAutoAdapter(source_file)
    monkeypatch.setattr("chatstrata.cli.load_adapters", lambda: {adapter.name: adapter})

    db_path = str(tmp_path / "archive.duckdb")
    runner = CliRunner()

    result1 = runner.invoke(cli, ["ingest", "--auto", "--no-embed", "--db", db_path])

    assert result1.exit_code == 0
    assert "stub_auto: 1 conversation found (full)" in result1.output
    assert "stub_auto" in result1.output
    assert "ingested=1 skipped=0 failed=0" in result1.output

    result2 = runner.invoke(cli, ["ingest", "--auto", "--no-embed", "--db", db_path])

    assert result2.exit_code == 0
    assert "stub_auto: 1 conversation found (incremental)" in result2.output
    assert "ingested=0 skipped=1 failed=0" in result2.output


def test_ingest_auto_dry_run_lists_detected_sources(tmp_path, monkeypatch):
    source_file = tmp_path / "session.jsonl"
    source_file.write_text("{}\n", encoding="utf-8")
    adapter = StubAutoAdapter(source_file)
    monkeypatch.setattr("chatstrata.cli.load_adapters", lambda: {adapter.name: adapter})

    runner = CliRunner()
    result = runner.invoke(cli, ["ingest", "--auto", "--dry-run"])

    assert result.exit_code == 0
    assert "Would auto-ingest detected sources:" in result.output
    assert "stub_auto" in result.output
