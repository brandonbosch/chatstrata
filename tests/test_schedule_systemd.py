"""Tests for the Linux systemd user-timer scheduler."""

from __future__ import annotations

import configparser
import subprocess

import pytest
from click.testing import CliRunner

from chatstrata.schedule import systemd
from chatstrata.schedule.cli import schedule


def _parse(text: str) -> configparser.ConfigParser:
    parser = configparser.ConfigParser(strict=False, interpolation=None)
    parser.optionxform = str  # keep key case
    parser.read_string(text)
    return parser


@pytest.fixture
def fake_systemctl(tmp_path, monkeypatch):
    """Redirect unit files to tmp_path and record systemctl calls."""
    unit_dir = tmp_path / "systemd" / "user"
    monkeypatch.setattr(systemd, "SERVICE_PATH", unit_dir / systemd.SERVICE_NAME)
    monkeypatch.setattr(systemd, "TIMER_PATH", unit_dir / systemd.TIMER_NAME)
    monkeypatch.setattr(systemd.shutil, "which", lambda name: f"/usr/bin/{name}")

    calls: list[tuple[str, ...]] = []
    state = {"active": "inactive"}

    def fake_run(cmd, **kwargs):
        assert cmd[:2] == ["systemctl", "--user"]
        args = tuple(cmd[2:])
        calls.append(args)
        stdout = ""
        if args[0] == "enable" or args[0] == "restart":
            state["active"] = "active"
        elif args[0] == "disable":
            state["active"] = "inactive"
        elif args[0] == "is-active":
            stdout = state["active"] + "\n"
        elif args[0] == "show":
            stdout = "ExecMainStartTimestamp=Sun 2026-10-04 12:00:00 UTC\nExecMainStatus=0\n"
        return subprocess.CompletedProcess(cmd, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(systemd.subprocess, "run", fake_run)
    return calls


def test_build_service_runs_ingest_auto():
    parser = _parse(systemd.build_service(binary="/opt/bin/chatstrata"))
    assert parser["Service"]["Type"] == "oneshot"
    assert parser["Service"]["ExecStart"] == '"/opt/bin/chatstrata" "ingest" "--auto"'


def test_build_service_no_embed_and_special_chars():
    text = systemd.build_service(binary="/opt/my dir/100%/chatstrata", no_embed=True)
    exec_line = next(line for line in text.splitlines() if line.startswith("ExecStart="))
    assert '"/opt/my dir/100%%/chatstrata"' in exec_line
    assert exec_line.endswith('"--no-embed"')


def test_build_timer_interval():
    parser = _parse(systemd.build_timer(interval_seconds=1800))
    assert parser["Timer"]["OnUnitActiveSec"] == "1800s"
    assert parser["Timer"]["Unit"] == systemd.SERVICE_NAME
    assert parser["Install"]["WantedBy"] == "timers.target"


def test_install_writes_units_and_enables_timer(fake_systemctl):
    path = systemd.install(interval_seconds=900, binary="/opt/bin/chatstrata")
    assert path == systemd.TIMER_PATH
    assert systemd.SERVICE_PATH.exists() and systemd.TIMER_PATH.exists()
    assert fake_systemctl == [
        ("daemon-reload",),
        ("enable", systemd.TIMER_NAME),
        ("restart", systemd.TIMER_NAME),
    ]


def test_status_round_trip(fake_systemctl):
    assert systemd.get_status()["installed"] is False

    systemd.install(interval_seconds=3600, binary="/opt/bin/chatstrata", no_embed=True)
    info = systemd.get_status()
    assert info["installed"] is True
    assert info["loaded"] is True
    assert info["interval_seconds"] == 3600
    assert info["binary"] == "/opt/bin/chatstrata"
    assert info["no_embed"] is True
    assert info["last_exit_status"] == 0


def test_uninstall_removes_units(fake_systemctl):
    systemd.install(interval_seconds=900, binary="/opt/bin/chatstrata")
    systemd.uninstall()
    assert not systemd.SERVICE_PATH.exists()
    assert not systemd.TIMER_PATH.exists()
    assert ("disable", "--now", systemd.TIMER_NAME) in fake_systemctl


def test_missing_systemctl_raises(tmp_path, monkeypatch):
    monkeypatch.setattr(systemd, "SERVICE_PATH", tmp_path / systemd.SERVICE_NAME)
    monkeypatch.setattr(systemd, "TIMER_PATH", tmp_path / systemd.TIMER_NAME)
    monkeypatch.setattr(systemd.shutil, "which", lambda name: None)
    with pytest.raises(FileNotFoundError, match="systemctl"):
        systemd.install(interval_seconds=900, binary="/opt/bin/chatstrata")


def test_cli_linux_install_status_uninstall(fake_systemctl, monkeypatch):
    monkeypatch.setattr("chatstrata.schedule.cli.platform.system", lambda: "Linux")
    runner = CliRunner()

    result = runner.invoke(
        schedule, ["install", "--interval", "30m", "--binary", "/opt/bin/chatstrata"]
    )
    assert result.exit_code == 0, result.output
    assert "every 30m" in result.output

    result = runner.invoke(schedule, ["status"])
    assert result.exit_code == 0, result.output
    assert "active" in result.output
    assert "Interval: 30m" in result.output

    result = runner.invoke(schedule, ["uninstall"])
    assert result.exit_code == 0, result.output
    assert "removed" in result.output

    result = runner.invoke(schedule, ["status"])
    assert "No scheduled sync installed" in result.output
