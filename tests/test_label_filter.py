"""Tests for the label-based content filter and its MCP wiring."""

from __future__ import annotations

import json

import duckdb
import pytest

from chatstrata.core.db import connect
from chatstrata.label.filter import (
    FilterError,
    FilterRule,
    apply_filter,
    check_query,
    parse_rules,
)
from chatstrata.label.packs import get_pack
from chatstrata.label.runner import run_pack, select_targets
from tests.test_label import FakeBackend

CYBER = "cyber:security_related>0.5"


def _label(db_path, pack_name, backend=None):
    conn = connect(db_path)
    pack = get_pack(pack_name)
    run_pack(conn, pack, backend or FakeBackend(), select_targets(conn, pack))
    conn.close()


def _filtered(db_path, rules):
    conn = duckdb.connect(str(db_path), read_only=True)
    apply_filter(conn, parse_rules(rules))
    return conn


def _blocks(conn, type_):
    return conn.execute(
        "SELECT id, text, payload FROM content_blocks WHERE type = ? ORDER BY id", [type_]
    ).fetchall()


class TestRules:
    def test_parse(self):
        assert parse_rules("cyber:security_related>0.3, cyber:posture=offensive") == [
            FilterRule("cyber", "security_related", ">", 0.3),
            FilterRule("cyber", "posture", "=", "offensive"),
        ]
        assert parse_rules("") == [] and parse_rules(None) == []

    @pytest.mark.parametrize("bad", ["cyber", "cyber:q<0.5", "cyber:q>high"])
    def test_parse_errors(self, bad):
        with pytest.raises(FilterError):
            parse_rules(bad)

    @pytest.mark.parametrize(
        "rule, match",
        [
            ("nope:x>0.5", "Unknown pack"),
            ("cyber:nope>0.5", "no question"),
            ("cyber:posture>0.5", "use '='"),
            ("cyber:security_related=true", "use '>'"),
        ],
    )
    def test_rule_must_fit_pack(self, db_path, rule, match):
        conn = duckdb.connect(str(db_path), read_only=True)
        with pytest.raises(FilterError, match=match):
            apply_filter(conn, parse_rules(rule))


class TestFilter:
    def test_unlabelled_tool_calls_are_hidden(self, db_path):
        conn = _filtered(db_path, CYBER)
        uses = _blocks(conn, "tool_use")
        assert uses
        for _, text, payload in uses:
            assert text == "[hidden by chatstrata filter: unlabelled:cyber]"
            assert payload is None
        # tool_results pair with their (hidden) calls
        assert all(t.startswith("[hidden") for _, t, _ in _blocks(conn, "tool_result"))
        # plain text is not covered by the tool-call pack
        assert not any((t or "").startswith("[hidden") for _, t, _ in _blocks(conn, "text"))

    def test_flagged_hidden_and_safe_visible(self, db_path):
        _label(db_path, "cyber")  # FakeBackend answers noul 0.8 for everything
        flagged = _filtered(db_path, CYBER)
        assert all(
            t == f"[hidden by chatstrata filter: {CYBER}]"
            for _, t, _ in _blocks(flagged, "tool_use")
        )
        flagged.close()

        safe = _filtered(db_path, "cyber:security_related>0.9")
        assert all(p is not None for _, _, p in _blocks(safe, "tool_use"))
        assert not any((t or "").startswith("[hidden") for _, t, _ in _blocks(safe, "tool_result"))

    def test_missing_label_fails_closed(self, db_path):
        _label(db_path, "cyber")
        conn = connect(db_path)
        dropped = conn.execute("SELECT target_id FROM labels LIMIT 1").fetchone()[0]
        conn.execute("DELETE FROM labels WHERE target_id = ?", [dropped])
        conn.close()

        conn = _filtered(db_path, "cyber:security_related>0.9")
        hidden = {
            i: t for i, t, _ in _blocks(conn, "tool_use") if (t or "").startswith("[hidden")
        }
        assert hidden == {dropped: "[hidden by chatstrata filter: unlabelled:cyber]"}

    def test_message_pack_hides_text_and_titles(self, db_path):
        conn = _filtered(db_path, "cyber-messages:security_related>0.5")
        texts = _blocks(conn, "text")
        assert texts and all(t.startswith("[hidden") for _, t, _ in texts)
        titles = conn.execute("SELECT DISTINCT title FROM conversations").fetchall()
        assert titles == [("[hidden by chatstrata filter]",)]

    def test_views_and_qualified_names_are_filtered(self, db_path):
        conn = _filtered(db_path, CYBER)
        assert conn.execute("SELECT COUNT(*) FROM tool_calls WHERE input IS NOT NULL").fetchone()[0] == 0
        assert conn.execute(
            "SELECT COUNT(*) FROM main.content_blocks WHERE type = 'tool_use' AND payload IS NOT NULL"
        ).fetchone()[0] == 0
        assert conn.execute("SELECT COUNT(*) FROM raw_events WHERE payload IS NOT NULL").fetchone()[0] == 0
        # shape is unchanged, so existing queries keep working
        raw = duckdb.connect(str(db_path), read_only=True)
        assert (
            [d[0] for d in conn.execute("SELECT * FROM content_blocks LIMIT 0").description]
            == [d[0] for d in raw.execute("SELECT * FROM content_blocks LIMIT 0").description]
        )
        assert conn.execute("SELECT COUNT(*) FROM content_blocks").fetchone()[0] == raw.execute(
            "SELECT COUNT(*) FROM content_blocks"
        ).fetchone()[0]

    @pytest.mark.parametrize(
        "sql",
        [
            "SELECT text FROM test.main.content_blocks",
            'SELECT text FROM "test".content_blocks',
            "SELECT term FROM fts_main_content_blocks.dict",
        ],
    )
    def test_check_query_blocks_bypasses(self, sql):
        with pytest.raises(ValueError, match="content filter is on"):
            check_query(sql, "test")

    def test_check_query_allows_normal_sql(self):
        check_query(
            "SELECT c.title FROM content_blocks cb JOIN messages m ON m.id = cb.message_id "
            "JOIN conversations c ON c.id = m.conversation_id "
            "WHERE fts_main_content_blocks.match_bm25(cb.id, 'x') IS NOT NULL",
            "test",
        )


class TestMcp:
    @pytest.fixture
    def server(self, db_path, monkeypatch):
        pytest.importorskip("mcp")
        from chatstrata.mcp import server

        monkeypatch.setattr(server, "get_default_db_path", lambda: db_path)
        monkeypatch.delenv(server.FILTER_ENV, raising=False)
        yield server
        server.set_filter(None)

    def _query(self, server, sql):
        return json.loads(server.query(sql))

    def test_filter_from_env(self, server, monkeypatch):
        monkeypatch.setenv(server.FILTER_ENV, CYBER)
        out = self._query(server, "SELECT text FROM content_blocks WHERE type = 'tool_use'")
        assert out["rows"] and all(r["text"].startswith("[hidden") for r in out["rows"])

    def test_bypasses_rejected(self, server):
        server.set_filter(parse_rules(CYBER))
        assert "error" in self._query(server, "SELECT text FROM test.main.content_blocks")
        out = self._query(server, "SELECT * FROM read_text('/etc/hostname')")
        assert "error" in out and "rows" not in out

    def test_bad_filter_refuses_instead_of_leaking(self, server):
        server.set_filter(parse_rules("nope:x>0.5"))
        out = self._query(server, "SELECT text FROM content_blocks")
        assert "Unknown pack" in out["error"] and "rows" not in out

    def test_no_filter_is_unchanged(self, server):
        out = self._query(server, "SELECT COUNT(*) AS n FROM content_blocks WHERE text LIKE '[hidden%'")
        assert out["rows"] == [{"n": 0}]

    def test_schema_mentions_filter(self, server):
        server.set_filter(parse_rules(CYBER))
        schema = server.get_schema()
        assert "## Content Filter" in schema and CYBER in schema
        assert "_cs_hidden" not in schema
