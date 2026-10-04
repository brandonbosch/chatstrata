"""Tests for `chatstrata label`: packs, target selection, runs and summaries."""

from __future__ import annotations

import json
from dataclasses import replace

import pytest
from click.testing import CliRunner

from chatstrata.cli import cli
from chatstrata.core.db import connect
from chatstrata.label import cli as label_cli
from chatstrata.label.backend import BackendError, BackendResult
from chatstrata.label.packs import TOOL_FAILURES, USER_TURNS, clip
from chatstrata.label.runner import (
    TargetFilters,
    clear_labels,
    count_labels,
    estimate_tokens,
    run_pack,
    select_targets,
    summarize,
)
from chatstrata.label.toml_packs import (
    PackFileError,
    discover_toml_packs,
    load_pack_file,
)

CYBER_TOML = """
name = "mini-cyber"
description = "test pack"
target_kind = "tool_call"
target_sql = "SELECT 1"

[state]
harness = { column = "source_id" }
args = { column = "arguments", unwrap = true, json = true, clip = 50 }
result = { column = "result_text", clip = 20, default = "(none)" }

[questions.security_related]
type = "noul"
instructions = "Security?"
"""

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


class TestTomlPacks:
    def _write(self, tmp_path, text, name="p.toml"):
        path = tmp_path / name
        path.write_text(text)
        return path

    def test_loads_valid_pack(self, tmp_path):
        pack = load_pack_file(self._write(tmp_path, CYBER_TOML))
        assert pack.name == "mini-cyber"
        assert pack.target_kind == "tool_call"
        assert pack.source.endswith("p.toml")
        assert pack.state_fingerprint is not None
        assert "security_related" in pack.questions

    def test_state_builder_transforms(self, tmp_path):
        pack = load_pack_file(self._write(tmp_path, CYBER_TOML))
        state = pack.build_state(
            {
                "source_id": "omp",
                "arguments": json.dumps({"input": {"cmd": "x" * 200}}),
                "result_text": None,
            }
        )
        assert state["harness"] == "omp"
        assert state["result"] == "(none)"  # default used for NULL
        # unwrapped out of the {"input": ...} envelope, rendered as JSON, clipped
        assert '"cmd"' in state["args"]
        assert "chars omitted" in state["args"]
        assert len(state["args"]) < 120  # far shorter than the 200-char input

    def test_state_fingerprint_changes_version(self, tmp_path):
        a = load_pack_file(self._write(tmp_path, CYBER_TOML, "a.toml"))
        b = load_pack_file(
            self._write(tmp_path, CYBER_TOML.replace("clip = 50", "clip = 80"), "b.toml")
        )
        assert a.version != b.version

    def test_missing_field(self, tmp_path):
        bad = CYBER_TOML.replace('target_kind = "tool_call"', "")
        with pytest.raises(PackFileError, match="target_kind"):
            load_pack_file(self._write(tmp_path, bad))

    def test_bad_target_kind(self, tmp_path):
        bad = CYBER_TOML.replace('target_kind = "tool_call"', 'target_kind = "nope"')
        with pytest.raises(PackFileError, match="target_kind must be one of"):
            load_pack_file(self._write(tmp_path, bad))

    def test_bad_question_type(self, tmp_path):
        bad = CYBER_TOML.replace('type = "noul"', 'type = "vibes"')
        with pytest.raises(PackFileError, match="type"):
            load_pack_file(self._write(tmp_path, bad))

    def test_unknown_state_option(self, tmp_path):
        bad = CYBER_TOML.replace("clip = 50", "clip = 50, bogus = 1")
        with pytest.raises(PackFileError, match="unknown options"):
            load_pack_file(self._write(tmp_path, bad))

    def test_discovery_and_get_pack(self, tmp_path, monkeypatch):
        self._write(tmp_path, CYBER_TOML)
        monkeypatch.setenv("CHATSTRATA_PACKS_DIR", str(tmp_path))
        assert "mini-cyber" in discover_toml_packs()
        from chatstrata.label.packs import get_pack

        assert get_pack("mini-cyber").name == "mini-cyber"

    def test_bundled_cyber_pack_is_discovered(self):
        assert "cyber" in discover_toml_packs()

    def test_run_toml_pack(self, conn, tmp_path, monkeypatch):
        # a real tool_call pack over the fixture DB, via the shared tool-call SQL
        toml = CYBER_TOML.replace("SELECT 1", TOOL_FAILURES.target_sql.replace("\n", " "))
        pack = load_pack_file(self._write(tmp_path, toml))
        targets = select_targets(conn, pack)
        assert targets
        stats = run_pack(conn, pack, FakeBackend(), targets)
        assert stats.labelled == len(targets)
        n = conn.execute("SELECT COUNT(*) FROM labels WHERE pack = 'mini-cyber'").fetchone()[0]
        assert n == len(targets)  # one noul question each

    def test_conversation_pack_runs_and_summarizes(self, conn, tmp_path):
        toml = CYBER_TOML.replace('target_kind = "tool_call"', 'target_kind = "conversation"')
        toml = toml.replace(
            'target_sql = "SELECT 1"',
            'target_sql = "SELECT id AS target_id, source_id, project, '
            'started_at AS created_at, title FROM conversations"',
        )
        pack = load_pack_file(self._write(tmp_path, toml))
        run_pack(conn, pack, FakeBackend(), select_targets(conn, pack))
        cols, rows = summarize(conn, pack, by="source")
        assert {r[cols.index("group")] for r in rows} == {"claude_code", "omp"}

    def test_target_sql_missing_required_columns(self, conn, tmp_path):
        toml = CYBER_TOML.replace(
            'target_sql = "SELECT 1"', 'target_sql = "SELECT id AS target_id FROM conversations"'
        )
        pack = load_pack_file(self._write(tmp_path, toml))
        with pytest.raises(ValueError, match="missing source_id, project, created_at"):
            select_targets(conn, pack)

    def test_broken_user_pack_is_skipped_with_warning(self, tmp_path, monkeypatch):
        self._write(tmp_path, CYBER_TOML, "good.toml")
        self._write(tmp_path, "name = [unterminated", "bad.toml")
        monkeypatch.setenv("CHATSTRATA_PACKS_DIR", str(tmp_path))

        with pytest.raises(PackFileError, match="bad.toml"):
            discover_toml_packs()
        errors: list[Exception] = []
        assert "mini-cyber" in discover_toml_packs(errors.append)
        assert len(errors) == 1 and "bad.toml" in str(errors[0])

        result = CliRunner().invoke(cli, ["label", "packs"])
        assert result.exit_code == 0, result.output
        assert "mini-cyber" in result.output and "tool-failures" in result.output
        assert "skipping pack file" in result.output and "bad.toml" in result.output


class TestClear:
    def test_clear_removes_only_that_pack(self, conn):
        run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        run_pack(conn, USER_TURNS, FakeBackend(), select_targets(conn, USER_TURNS))
        before = conn.execute("SELECT COUNT(*) FROM labels WHERE pack = 'user-turns'").fetchone()[0]

        scope = clear_labels(conn, "tool-failures")

        assert scope.labels > 0 and scope.runs == 1
        assert conn.execute("SELECT COUNT(*) FROM labels WHERE pack = 'tool-failures'").fetchone()[0] == 0
        assert conn.execute("SELECT COUNT(*) FROM label_runs WHERE pack = 'tool-failures'").fetchone()[0] == 0
        # the other pack is untouched
        assert conn.execute("SELECT COUNT(*) FROM labels WHERE pack = 'user-turns'").fetchone()[0] == before

    def test_count_does_not_delete(self, conn):
        run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        scope = count_labels(conn, "tool-failures")
        assert scope.labels > 0
        assert conn.execute("SELECT COUNT(*) FROM labels").fetchone()[0] == scope.labels

    def test_clear_by_version(self, conn):
        run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        assert clear_labels(conn, "tool-failures", version="nope").labels == 0
        assert conn.execute("SELECT COUNT(*) FROM labels").fetchone()[0] > 0
        assert clear_labels(conn, "tool-failures", version=TOOL_FAILURES.version).labels > 0
        assert conn.execute("SELECT COUNT(*) FROM labels").fetchone()[0] == 0

    def test_clear_by_run(self, conn):
        stats = run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        scope = clear_labels(conn, "tool-failures", run_id=stats.run_id)
        assert scope.labels > 0 and scope.runs == 1
        assert conn.execute("SELECT COUNT(*) FROM label_runs").fetchone()[0] == 0

    def test_clear_by_version_and_run(self, conn):
        first = run_pack(conn, TOOL_FAILURES, FakeBackend(), select_targets(conn, TOOL_FAILURES))
        run_pack(conn, USER_TURNS, FakeBackend(), select_targets(conn, USER_TURNS))
        scope = clear_labels(
            conn, "tool-failures", version=TOOL_FAILURES.version, run_id=first.run_id
        )
        assert scope.labels > 0 and scope.runs == 1
        assert conn.execute(
            "SELECT COUNT(*) FROM label_runs WHERE pack = 'user-turns'"
        ).fetchone()[0] == 1

    def test_cli_dry_run_keeps_labels(self, db_path):
        c = connect(db_path)
        run_pack(c, TOOL_FAILURES, FakeBackend(), select_targets(c, TOOL_FAILURES))
        c.close()
        result = CliRunner().invoke(
            cli, ["label", "clear", "tool-failures", "--dry-run", "--db", str(db_path)]
        )
        assert result.exit_code == 0 and "match" in result.output
        c = connect(db_path)
        assert c.execute("SELECT COUNT(*) FROM labels").fetchone()[0] > 0
        c.close()

    def test_cli_nothing_matches(self, db_path):
        result = CliRunner().invoke(
            cli, ["label", "clear", "tool-failures", "--yes", "--db", str(db_path)]
        )
        assert result.exit_code == 0 and "nothing matched" in result.output


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
