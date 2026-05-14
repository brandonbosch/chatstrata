"""Tests for chatstrata analyze CLI commands."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
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


@pytest.fixture
def runner():
    return CliRunner()


class TestAnalyzeActivity:
    def test_default_monthly(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "activity", "--db", str(populated_db)])
        assert result.exit_code == 0
        assert "claude_code" in result.output
        assert "messages" in result.output

    def test_by_day(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "activity", "--by", "day", "--db", str(populated_db)])
        assert result.exit_code == 0
        assert "claude_code" in result.output

    def test_by_week(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "activity", "--by", "week", "--db", str(populated_db)])
        assert result.exit_code == 0

    def test_source_filter(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "activity", "--source", "claude_code", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        assert "claude_code" in result.output

    def test_source_filter_no_match(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "activity", "--source", "nonexistent", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        assert "No data" in result.output

    def test_json_output(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "activity", "--json", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert isinstance(data, list)
        assert len(data) > 0
        assert "source" in data[0]
        assert "messages" in data[0]

    def test_invalid_granularity_rejected(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "activity", "--by", "year", "--db", str(populated_db)],
        )
        assert result.exit_code != 0


class TestAnalyzeTools:
    def test_default(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "tools", "--db", str(populated_db)])
        assert result.exit_code == 0
        assert "view" in result.output

    def test_source_filter(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "tools", "--source", "claude_code", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        assert "view" in result.output

    def test_json_output(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "tools", "--json", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert any(row["tool_name"] == "view" for row in data)


class TestAnalyzeConversations:
    def test_default_longest(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "conversations", "--db", str(populated_db)])
        assert result.exit_code == 0
        assert "Refactor" in result.output

    def test_longest_n(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "conversations", "--longest", "5", "--db", str(populated_db)],
        )
        assert result.exit_code == 0

    def test_shortest_n(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "conversations", "--shortest", "5", "--db", str(populated_db)],
        )
        assert result.exit_code == 0

    def test_both_flags_error(self, runner, populated_db):
        result = runner.invoke(
            cli,
            ["analyze", "conversations", "--longest", "5", "--shortest", "3", "--db", str(populated_db)],
        )
        assert result.exit_code != 0

    def test_json_output(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "conversations", "--json", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert isinstance(data, list)
        assert len(data) > 0


class TestAnalyzeModels:
    def test_default(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "models", "--db", str(populated_db)])
        assert result.exit_code == 0
        assert "claude-opus-4-7" in result.output

    def test_json_output(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "models", "--json", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert any(row["model"] == "claude-opus-4-7" for row in data)


class TestAnalyzeProjects:
    def test_default(self, runner, populated_db):
        result = runner.invoke(cli, ["analyze", "projects", "--db", str(populated_db)])
        assert result.exit_code == 0
        assert "/Users/example/myproj" in result.output

    def test_json_output(self, runner, populated_db):
        result = runner.invoke(
            cli, ["analyze", "projects", "--json", "--db", str(populated_db)],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert any(row["project"] == "/Users/example/myproj" for row in data)


class TestAnalyzeGroup:
    def test_help(self, runner):
        result = runner.invoke(cli, ["analyze", "--help"])
        assert result.exit_code == 0
        assert "activity" in result.output
        assert "tools" in result.output
        assert "conversations" in result.output
        assert "models" in result.output
        assert "projects" in result.output

    def test_no_subcommand_shows_usage(self, runner):
        result = runner.invoke(cli, ["analyze"])
        assert "Usage" in result.output
