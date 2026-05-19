"""Tests for the schema migration framework."""

import duckdb
import pytest

from chatstrata.core.db import _load_vss_extension, apply_migrations, connect, get_schema_version
from chatstrata.core.migrations import LATEST_VERSION


@pytest.fixture
def raw_conn(tmp_path):
    """A DuckDB connection with no auto-migration."""
    conn = connect(tmp_path / "test.duckdb", auto_migrate=False)
    yield conn
    conn.close()


@pytest.fixture
def db(tmp_path):
    """A fully migrated DuckDB connection."""
    conn = connect(tmp_path / "test.duckdb")
    yield conn
    conn.close()


def test_fresh_db_version_is_zero(raw_conn):
    assert get_schema_version(raw_conn) == 0


def test_latest_version():
    assert LATEST_VERSION == 3


def test_apply_migrations_on_fresh_db(raw_conn):
    applied = apply_migrations(raw_conn)
    assert len(applied) == 3
    assert applied[0].version == 1
    assert applied[1].version == 2
    assert applied[2].version == 3
    assert get_schema_version(raw_conn) == 3

    tables = {
        row[0]
        for row in raw_conn.execute(
            "SELECT table_name FROM information_schema.tables WHERE table_schema = 'main'"
        ).fetchall()
    }
    for expected in ("meta", "sources", "conversations", "messages", "content_blocks"):
        assert expected in tables


def test_migrations_are_idempotent(raw_conn):
    first = apply_migrations(raw_conn)
    assert len(first) == 3

    second = apply_migrations(raw_conn)
    assert second == []
    assert get_schema_version(raw_conn) == 3


def test_migration_0003_adds_mtime_column(raw_conn):
    apply_migrations(raw_conn)
    cols = {
        row[0]
        for row in raw_conn.execute(
            "SELECT column_name FROM information_schema.columns "
            "WHERE table_name = 'conversations'"
        ).fetchall()
    }
    assert "source_file_mtime" in cols


def test_auto_migrate_on_connect(db):
    assert get_schema_version(db) == LATEST_VERSION


class FakeVssConnection:
    def __init__(self):
        self.statements = []

    def execute(self, sql):
        self.statements.append(sql)
        raise duckdb.CatalogException("extension not found")


def test_vss_install_is_opt_in(monkeypatch):
    monkeypatch.delenv("CHATSTRATA_INSTALL_DUCKDB_VSS", raising=False)
    conn = FakeVssConnection()

    _load_vss_extension(conn)

    assert conn.statements == ["LOAD vss"]


def test_vss_install_runs_when_enabled(monkeypatch):
    monkeypatch.setenv("CHATSTRATA_INSTALL_DUCKDB_VSS", "1")
    conn = FakeVssConnection()

    _load_vss_extension(conn)

    assert conn.statements == ["LOAD vss", "INSTALL vss; LOAD vss;"]
