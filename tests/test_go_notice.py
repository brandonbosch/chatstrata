"""The 0.5 notice that points to the Go app."""

from __future__ import annotations

from click.testing import CliRunner

from chatstrata import cli as cli_module
from chatstrata.cli import GO_NOTICE, cli


def _run(monkeypatch, *, tty: bool, env: dict[str, str] | None = None):
    monkeypatch.setattr(cli_module, "_stderr_is_terminal", lambda: tty)
    return CliRunner().invoke(cli, ["sources"], env=env or {})


def test_notice_on_a_terminal(monkeypatch):
    monkeypatch.delenv("CHATSTRATA_NO_GO_NOTICE", raising=False)
    result = _run(monkeypatch, tty=True)
    assert result.exit_code == 0
    assert GO_NOTICE in result.output


def test_no_notice_when_not_a_terminal(monkeypatch):
    monkeypatch.delenv("CHATSTRATA_NO_GO_NOTICE", raising=False)
    result = _run(monkeypatch, tty=False)
    assert result.exit_code == 0
    assert GO_NOTICE not in result.output


def test_notice_can_be_hidden(monkeypatch):
    result = _run(monkeypatch, tty=True, env={"CHATSTRATA_NO_GO_NOTICE": "1"})
    assert result.exit_code == 0
    assert GO_NOTICE not in result.output
