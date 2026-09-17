"""A source that cannot be read must report why, never read as empty.

Regression for issue #18: hermes_agent swallowed a missing state database so
`chatstrata ingest hermes_agent` printed "No conversations found", making an
installed Hermes Agent indistinguishable from an absent one.
"""

from __future__ import annotations

import sqlite3

import pytest
from click.testing import CliRunner

from chatstrata.cli import cli


@pytest.fixture
def hermes_home(tmp_path, monkeypatch):
    """An isolated Hermes root, so discovery never touches the real ~/.hermes."""
    home = tmp_path / "hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _output(result) -> str:
    """stdout + stderr; clicks 8.2+ captures them separately."""
    return result.output + (result.stderr or "")


def test_ingest_reports_a_missing_state_database(hermes_home):
    result = CliRunner().invoke(cli, ["ingest", "hermes_agent", "--dry-run"])

    assert result.exit_code == 1
    assert "Hermes state database not found" in _output(result)
    assert str(hermes_home / "state.db") in _output(result)
    assert "No conversations found" not in _output(result)


def test_ingest_reports_a_readable_store_that_is_empty(hermes_home):
    conn = sqlite3.connect(hermes_home / "state.db")
    conn.execute(
        "CREATE TABLE sessions (id TEXT, source TEXT, model TEXT, title TEXT,"
        " started_at REAL, ended_at REAL, message_count INTEGER, cwd TEXT,"
        " display_name TEXT)"
    )
    conn.commit()
    conn.close()

    result = CliRunner().invoke(cli, ["ingest", "hermes_agent", "--dry-run"])

    assert result.exit_code == 0
    assert "No conversations found for source 'hermes_agent'" in _output(result)


def test_init_reports_an_unavailable_source(tmp_path, hermes_home):
    result = CliRunner().invoke(cli, ["init", "--db", str(tmp_path / "archive.duckdb")])

    out = _output(result)
    assert result.exit_code == 0
    assert "hermes_agent" in out
    assert "not available" in out
    assert "Hermes state database not found" in out


def test_auto_names_the_sources_it_skipped(monkeypatch):
    class Unreadable:
        name = "broken_source"
        display_name = "Broken"
        version = "0.0.1"

        def discover(self, config=None):
            raise FileNotFoundError("Hermes state database not found: /nope/state.db")

    class Empty:
        name = "empty_source"
        display_name = "Empty"
        version = "0.0.1"

        def discover(self, config=None):
            return []

    monkeypatch.setattr(
        "chatstrata.cli.load_adapters",
        lambda: {"broken_source": Unreadable(), "empty_source": Empty()},
    )

    result = CliRunner().invoke(cli, ["ingest", "--auto"])

    out = _output(result)
    assert "failed to discover broken_source: Hermes state database not found" in out
    assert "skipped empty_source: no conversations found" in out
