"""Tests for chatstrata redact CLI commands."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

try:
    from chatstrata.redact.presidio_engine import PresidioEngine  # noqa: F401

    HAS_PRESIDIO = True
except ImportError:
    HAS_PRESIDIO = False

from click.testing import CliRunner

from chatstrata.cli import cli
from chatstrata.core.db import connect
from chatstrata.core.ingest import ensure_source, ingest_conversation
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
def runner():
    return CliRunner()


@pytest.fixture
def populated_db(tmp_path):
    db_path = tmp_path / "test.duckdb"
    conn = connect(db_path)
    adapter = ClaudeCodeAdapter()
    handle = ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
        metadata={"project": "/Users/example/myproj"},
    )
    ensure_source(conn, adapter.name, adapter.display_name, adapter.version)
    conv = adapter.parse(handle)
    ingest_conversation(conn, adapter.name, conv)
    conn.close()
    return db_path


class TestRedactGroup:
    def test_help_shows_subcommands(self, runner):
        result = runner.invoke(cli, ["redact", "--help"])
        assert result.exit_code == 0
        assert "text" in result.output
        assert "query" in result.output
        assert "interactive" in result.output

    def test_no_subcommand_shows_usage(self, runner):
        result = runner.invoke(cli, ["redact"])
        assert "Usage" in result.output


@pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")
class TestRedactText:
    def test_basic_mask(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "my email is test@example.com"],
        )
        assert result.exit_code == 0
        assert "[EMAIL_ADDRESS_1]" in result.output
        assert "test@example.com" not in result.output.split("\n")[0]

    def test_detect_only(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "my email is test@example.com", "--mode", "detect_only"],
        )
        assert result.exit_code == 0
        assert "test@example.com" in result.output

    def test_tag_mode(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "my email is test@example.com", "--mode", "tag"],
        )
        assert result.exit_code == 0
        assert "<PII:EMAIL_ADDRESS>" in result.output

    def test_remove_mode(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "my email is test@example.com", "--mode", "remove"],
        )
        assert result.exit_code == 0
        redacted_line = result.output.strip().split("\n")[0]
        assert "test@example.com" not in redacted_line

    def test_hash_mode(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "my email is test@example.com", "--mode", "hash"],
        )
        assert result.exit_code == 0
        redacted_line = result.output.strip().split("\n")[0]
        assert "test@example.com" not in redacted_line

    def test_json_output(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "my email is test@example.com", "--json"],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert "original" in data
        assert "redacted" in data
        assert "entities" in data
        assert "mapping" in data
        assert isinstance(data["entities"], list)

    def test_api_key_detection(self, runner):
        key = "sk-ant-api03-" + "A" * 80
        result = runner.invoke(cli, ["redact", "text", f"key: {key}"])
        assert result.exit_code == 0
        assert "[ANTHROPIC_API_KEY_1]" in result.output


@pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")
class TestRedactQuery:
    def test_basic_query_redact(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "query",
                "SELECT text FROM content_blocks WHERE text IS NOT NULL LIMIT 3",
                "--db",
                str(populated_db),
            ],
        )
        assert result.exit_code == 0

    def test_json_output(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "query",
                "SELECT text FROM content_blocks WHERE text IS NOT NULL LIMIT 3",
                "--db",
                str(populated_db),
                "--json",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert isinstance(data, list)

    def test_empty_result(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "query",
                "SELECT text FROM content_blocks WHERE 1=0",
                "--db",
                str(populated_db),
            ],
        )
        assert result.exit_code == 0
        assert "No results" in result.output

    def test_mutating_query_rejected(self, runner, populated_db):
        result = runner.invoke(
            cli,
            ["redact", "query", "DROP TABLE conversations", "--db", str(populated_db)],
        )
        assert result.exit_code != 0
        assert "DROP queries are not allowed" in result.output


@pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")
class TestRedactInteractive:
    def test_quit_immediately(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "interactive",
                "--db",
                str(populated_db),
                "--sql",
                "SELECT text FROM content_blocks WHERE text IS NOT NULL LIMIT 1",
            ],
            input="q",
        )
        assert result.exit_code == 0

    def test_skip_entity(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "interactive",
                "--db",
                str(populated_db),
                "--sql",
                "SELECT text FROM content_blocks WHERE text IS NOT NULL LIMIT 1",
            ],
            input="s" * 20 + "q",
        )
        assert result.exit_code == 0
        assert "Skipped:" in result.output

    def test_empty_results(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "interactive",
                "--db",
                str(populated_db),
                "--sql",
                "SELECT text FROM content_blocks WHERE 1=0",
            ],
        )
        assert result.exit_code == 0
        assert "No results" in result.output

    def test_mutating_sql_rejected(self, runner, populated_db):
        result = runner.invoke(
            cli,
            [
                "redact",
                "interactive",
                "--db",
                str(populated_db),
                "--sql",
                "DROP TABLE conversations",
            ],
        )
        assert result.exit_code != 0
        assert "DROP queries are not allowed" in result.output
