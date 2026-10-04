"""Tests for `chatstrata label`: packs, target selection, runs and summaries."""

from __future__ import annotations

import json
from dataclasses import replace
from pathlib import Path

import pytest
from click.testing import CliRunner

from chatstrata.cli import cli
from chatstrata.core.db import connect
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.models import ConversationHandle
from chatstrata.label import cli as label_cli
from chatstrata.label.backend import BackendError, BackendResult
from chatstrata.label.packs import TOOL_FAILURES, USER_TURNS, clip
from chatstrata.label.runner import (
    TargetFilters,
    estimate_tokens,
    run_pack,
    select_targets,
    summarize,
)
from chatstrata.sources.claude_code.adapter import ClaudeCodeAdapter
from chatstrata.sources.omp.adapter import OmpAdapter

SOURCES = Path(__file__).parent.parent / "chatstrata" / "sources"


@pytest.fixture
def db_path(tmp_path):
    path = tmp_path / "test.duckdb"
    conn = connect(path)
    for adapter, fixture in (
        (ClaudeCodeAdapter(), SOURCES / "claude_code/tests/fixtures/sample_session.jsonl"),
        (OmpAdapter(), SOURCES / "omp/tests/fixtures/sample_session.jsonl"),
    ):
        handle = ConversationHandle(
            source_native_id=fixture.stem, path=fixture, metadata={"project": "/p"}
        )
        ensure_source(conn, adapter.name, adapter.display_name, adapter.version)
        ingest_conversation(conn, adapter.name, adapter.parse(handle))
    conn.close()
    return path


@pytest.fixture
def conn(db_path):
    c = connect(db_path)
    yield c
    c.close()


class FakeBackend:
    """Answers every question deterministically, by type."""

    name = "fake"

    def __init__(self, fail_on: set[str] | None = None, fatal: bool = False):
        self.calls: list[dict] = []
        self.fail_on = fail_on or set()
        self.fatal = fatal

    def ask(self, state, questions):
        if self.fatal:
            raise BackendError("bad key")
        self.calls.append(state)
        if state.get("tool_name") in self.fail_on:
            raise RuntimeError("boom")
        answers = {}
        for qid, q in questions.items():
            if q["type"] == "noul":
                answers[qid] = {"type": "noul", "noul": 0.8}
            elif q["type"] == "choice":
                options = list(q["criteria"])
                probs = {o: 0.0 for o in options}
                probs[options[1]] = 1.0
                answers[qid] = {
                    "type": "choice", "choice": options[1], "confidence": 0.9,
                    "probabilities": probs,
                }
            else:
                answers[qid] = {
                    "type": "score", "score": 1.0, "confidence": 0.7,
                    "probabilities": {str(i): 0.0 for i in range(len(q["criteria"]))},
                }
        return BackendResult(model="jev-test", answers=answers, input_tokens=100)


class TestClip:
    def test_short_text_unchanged(self):
        assert clip("abc", 10) == "abc"
        assert clip(None, 10) is None

    def test_keeps_head_and_tail(self):
        text = "HEAD" + "x" * 1000 + "TAIL"
        out = clip(text, 100)
        assert out.startswith("HEAD") and out.endswith("TAIL")
        assert "chars omitted" in out


class TestSelectTargets:
    def test_tool_calls_pair_use_with_result(self, conn):
        targets = select_targets(conn, TOOL_FAILURES)
        by_harness = {t.state["harness"]: t.state for t in targets}
        assert set(by_harness) == {"claude_code", "omp"}
        omp = by_harness["omp"]
        assert omp["tool_name"] == "read"
        assert omp["result"] == "14 session files found"
        # The adapter's {"arguments": ...} envelope is unwrapped.
        assert json.loads(omp["arguments"]) == {"path": "/home/brandon/.omp/agent/sessions"}

    def test_user_turns_carry_previous_assistant(self, conn):
        targets = select_targets(conn, USER_TURNS, TargetFilters(source="omp"))
        states = [t.state for t in targets]
        assert states[0]["previous_assistant_message"] == "(none: first turn)"
        assert states[-1]["user_message"] == "plain string user turn"
        # omp stores the extension custom_message as an assistant turn.
        assert states[-1]["previous_assistant_message"] == "Injected context from extension"

    def test_filters(self, conn):
        assert len(select_targets(conn, TOOL_FAILURES, TargetFilters(source="omp"))) == 1
        assert select_targets(conn, TOOL_FAILURES, TargetFilters(tool="nope")) == []
        with pytest.raises(ValueError, match="tool_call"):
            select_targets(conn, USER_TURNS, TargetFilters(tool="read"))

    def test_estimate_tokens_is_positive(self, conn):
        targets = select_targets(conn, TOOL_FAILURES)
        assert estimate_tokens(TOOL_FAILURES, targets) > 0


class TestRunPack:
    def test_writes_labels_and_run(self, conn):
        targets = select_targets(conn, TOOL_FAILURES)
        stats = run_pack(conn, TOOL_FAILURES, FakeBackend(), targets, concurrency=2)

        assert stats.labelled == len(targets) and stats.failures == 0
        assert stats.input_tokens == 100 * len(targets)
        rows = conn.execute(
            "SELECT question, answer_type, value, choice, confidence, model "
            "FROM labels ORDER BY target_id, question"
        ).fetchall()
        assert len(rows) == 3 * len(targets)
        assert ("failure_mode", "choice", None, "match_failed", 0.9, "jev-test") in rows
        assert ("agent_caused", "noul", 0.8, None, None, "jev-test") in rows

        run = conn.execute(
            "SELECT pack, model, targets, failures, input_tokens, finished_at IS NOT NULL "
            "FROM label_runs"
        ).fetchone()
        assert run == ("tool-failures", "jev-test", len(targets), 0, 100 * len(targets), True)

    def test_rerun_skips_labelled_targets(self, conn):
        run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        assert select_targets(conn, TOOL_FAILURES) == []
        assert len(select_targets(conn, TOOL_FAILURES, relabel=True)) == 2

    def test_editing_questions_invalidates_labels(self, conn):
        run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        edited = replace(TOOL_FAILURES, questions={
            **TOOL_FAILURES.questions,
            "extra": {"type": "noul", "instructions": "Is this new?"},
        })
        assert edited.version != TOOL_FAILURES.version
        assert len(select_targets(conn, edited)) == 2

    def test_per_target_failures_are_counted(self, conn):
        targets = select_targets(conn, TOOL_FAILURES)
        stats = run_pack(conn, TOOL_FAILURES, FakeBackend(fail_on={"read"}), targets)
        assert stats.labelled == 1 and stats.failures == 1
        assert "boom" in stats.errors[0]

    def test_backend_error_aborts(self, conn):
        targets = select_targets(conn, TOOL_FAILURES)
        with pytest.raises(BackendError):
            run_pack(conn, TOOL_FAILURES, FakeBackend(fatal=True), targets)
        assert conn.execute("SELECT COUNT(*) FROM labels").fetchone()[0] == 0
        assert conn.execute("SELECT finished_at IS NOT NULL FROM label_runs").fetchone()[0]


class TestSummarize:
    def test_by_tool(self, conn):
        run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        cols, rows = summarize(conn, TOOL_FAILURES, by="tool")
        assert cols == ["group", "question", "answer", "n", "share_pct", "mean", "avg_conf"]
        assert ("read", "failure_mode", "match_failed", 1, 100.0, None, 0.9) in rows
        assert ("read", "agent_caused", "yes (p > 0.5)", 1, 100.0, 0.8, None) in rows

    def test_score_and_confidence_floor(self, conn):
        run_pack(conn, USER_TURNS, FakeBackend(), select_targets(conn, USER_TURNS))
        _, rows = summarize(conn, USER_TURNS, by="source")
        assert any(r[1] == "frustration" and r[2] == "score" and r[5] == 1.0 for r in rows)
        _, rows = summarize(conn, USER_TURNS, min_confidence=0.95)
        assert {r[1] for r in rows} == {"states_done_criteria"}

    def test_unknown_dimension(self, conn):
        with pytest.raises(ValueError, match="--by must be one of"):
            summarize(conn, USER_TURNS, by="tool")


class TestCli:
    def test_packs(self):
        result = CliRunner().invoke(cli, ["label", "packs"])
        assert result.exit_code == 0
        assert "tool-failures" in result.output and "user-turns" in result.output

    def test_dry_run_sends_nothing(self, db_path, monkeypatch):
        def explode(**_):
            raise AssertionError("backend must not be created on --dry-run")

        monkeypatch.setattr(label_cli, "TypeSafeBackend", explode)
        result = CliRunner().invoke(cli, [
            "label", "run", "tool-failures", "--dry-run", "--show-state", "--db", str(db_path),
        ])
        assert result.exit_code == 0, result.output
        assert "2 items" in result.output and '"harness"' in result.output

    def test_run_requires_confirmation(self, db_path, monkeypatch):
        monkeypatch.setattr(label_cli, "TypeSafeBackend", lambda **_: FakeBackend())
        result = CliRunner().invoke(
            cli, ["label", "run", "tool-failures", "--db", str(db_path)], input="n\n"
        )
        assert result.exit_code == 1
        conn = connect(db_path)
        assert conn.execute("SELECT COUNT(*) FROM labels").fetchone()[0] == 0
        conn.close()

    def test_run_and_summary(self, db_path, monkeypatch):
        monkeypatch.setattr(label_cli, "TypeSafeBackend", lambda **_: FakeBackend())
        runner = CliRunner()
        result = runner.invoke(cli, [
            "label", "run", "tool-failures", "--yes", "--source", "omp", "--db", str(db_path),
        ])
        assert result.exit_code == 0, result.output
        assert "Labelled 1 items with jev-test" in result.output

        result = runner.invoke(cli, [
            "label", "summary", "tool-failures", "--by", "source", "--json", "--db", str(db_path),
        ])
        assert result.exit_code == 0, result.output
        rows = json.loads(result.output)
        assert {r["group"] for r in rows} == {"omp"}

    def test_unknown_pack(self):
        result = CliRunner().invoke(cli, ["label", "summary", "nope"])
        assert result.exit_code == 2
        assert "Unknown pack" in result.output

    def test_missing_api_key(self, db_path, monkeypatch):
        pytest.importorskip("typesafe_sdk")
        monkeypatch.delenv("TYPESAFE_API_KEY", raising=False)
        result = CliRunner().invoke(cli, [
            "label", "run", "tool-failures", "--yes", "--db", str(db_path),
        ])
        assert result.exit_code == 1
        assert "TYPESAFE_API_KEY" in result.output


def test_typesafe_backend_round_trip():
    pytest.importorskip("typesafe_sdk")
    httpx2 = pytest.importorskip("httpx2")
    from typesafe_sdk import TypeSafeClient

    from chatstrata.label.backend import TypeSafeBackend

    sent = {}

    def handler(request):
        sent.update(json.loads(request.content))
        return httpx2.Response(200, json={
            "model": "jev-1.13.0",
            "answers": {"q": {"type": "noul", "noul": 0.25}},
            "usage": {"input_tokens": 42, "output_tokens": 5},
        })

    backend = TypeSafeBackend(api_key="test-key")
    backend._client = TypeSafeClient(api_key="test-key", transport=httpx2.MockTransport(handler))
    result = backend.ask({"x": 1}, {"q": {"type": "noul", "instructions": "Is it?"}})

    assert sent["state"] == {"x": 1}
    assert sent["questions"]["q"]["instructions"] == "Is it?"
    assert result == BackendResult(
        model="jev-1.13.0", answers={"q": {"type": "noul", "noul": 0.25}}, input_tokens=42
    )
