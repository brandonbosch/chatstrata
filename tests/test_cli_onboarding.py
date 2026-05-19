"""Tests for first-run CLI onboarding commands."""

from __future__ import annotations

from click.testing import CliRunner

from chatstrata.cli import cli
from chatstrata.core.db import connect, get_schema_version
from chatstrata.core.migrations import LATEST_VERSION


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
