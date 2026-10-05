"""Linux systemd user-timer integration for periodic chatstrata sync."""

from __future__ import annotations

import os
import re
import shlex
import shutil
import subprocess
from pathlib import Path

from chatstrata.schedule.launchd import _find_chatstrata_binary

UNIT_NAME = "chatstrata-sync"
SERVICE_NAME = f"{UNIT_NAME}.service"
TIMER_NAME = f"{UNIT_NAME}.timer"


def _unit_dir() -> Path:
    config_home = os.environ.get("XDG_CONFIG_HOME")
    base = Path(config_home) if config_home else Path.home() / ".config"
    return base / "systemd" / "user"


UNIT_DIR = _unit_dir()
SERVICE_PATH = UNIT_DIR / SERVICE_NAME
TIMER_PATH = UNIT_DIR / TIMER_NAME


def _quote(arg: str) -> str:
    """Quote one ExecStart argument. systemd expands `%` and `$`, so escape both."""
    escaped = arg.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%").replace("$", "$$")
    return f'"{escaped}"'


def build_service(*, binary: str | None = None, no_embed: bool = False) -> str:
    binary = binary or _find_chatstrata_binary()
    args = [binary, "ingest", "--auto"]
    if no_embed:
        args.append("--no-embed")
    exec_start = " ".join(_quote(a) for a in args)

    return (
        "[Unit]\n"
        "Description=chatstrata background sync\n"
        "\n"
        "[Service]\n"
        "Type=oneshot\n"
        f"ExecStart={exec_start}\n"
        "Nice=10\n"
        "IOSchedulingClass=idle\n"
    )


def build_timer(*, interval_seconds: int) -> str:
    # OnActiveSec fires shortly after the timer starts (the launchd RunAtLoad
    # equivalent); OnBootSec catches a fresh login; OnUnitActiveSec repeats.
    return (
        "[Unit]\n"
        "Description=Run chatstrata background sync periodically\n"
        "\n"
        "[Timer]\n"
        "OnActiveSec=10s\n"
        "OnBootSec=2min\n"
        f"OnUnitActiveSec={interval_seconds}s\n"
        f"Unit={SERVICE_NAME}\n"
        "\n"
        "[Install]\n"
        "WantedBy=timers.target\n"
    )


def _systemctl(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    if shutil.which("systemctl") is None:
        raise FileNotFoundError(
            "Cannot find 'systemctl'. Background sync on Linux requires systemd; "
            "on other init systems use cron: `*/15 * * * * chatstrata ingest --auto`."
        )
    return subprocess.run(
        ["systemctl", "--user", *args],
        capture_output=True,
        text=True,
        check=check,
    )


def install(*, interval_seconds: int, binary: str | None = None, no_embed: bool = False) -> Path:
    service = build_service(binary=binary, no_embed=no_embed)
    timer = build_timer(interval_seconds=interval_seconds)

    SERVICE_PATH.parent.mkdir(parents=True, exist_ok=True)
    SERVICE_PATH.write_text(service)
    TIMER_PATH.write_text(timer)

    _systemctl("daemon-reload")
    # enable --now on an already-active timer would not pick up a new
    # interval, so restart it explicitly.
    _systemctl("enable", TIMER_NAME)
    _systemctl("restart", TIMER_NAME)
    return TIMER_PATH


def uninstall() -> None:
    if shutil.which("systemctl") is not None:
        _systemctl("disable", "--now", TIMER_NAME, check=False)
    for path in (TIMER_PATH, SERVICE_PATH):
        path.unlink(missing_ok=True)
    if shutil.which("systemctl") is not None:
        _systemctl("daemon-reload", check=False)


def is_active() -> bool:
    return _systemctl("is-active", TIMER_NAME, check=False).stdout.strip() == "active"


def _show(unit: str, *props: str) -> dict[str, str]:
    args = ["show", unit, *(f"--property={p}" for p in props)]
    result = _systemctl(*args, check=False)
    values: dict[str, str] = {}
    for line in result.stdout.splitlines():
        key, sep, value = line.partition("=")
        if sep:
            values[key] = value
    return values


def get_status() -> dict:
    installed = TIMER_PATH.exists()
    status: dict = {
        "installed": installed,
        "loaded": False,
        "timer_path": str(TIMER_PATH),
        "service_path": str(SERVICE_PATH),
        "log_command": f"journalctl --user -u {SERVICE_NAME}",
    }
    if not installed:
        return status

    if SERVICE_PATH.exists():
        for line in SERVICE_PATH.read_text().splitlines():
            if line.startswith("ExecStart="):
                try:
                    argv = shlex.split(line[len("ExecStart=") :])
                except ValueError:
                    break
                status["binary"] = argv[0] if argv else None
                status["no_embed"] = "--no-embed" in argv
                break

    match = re.search(r"^OnUnitActiveSec=(\d+)s$", TIMER_PATH.read_text(), re.MULTILINE)
    if match:
        status["interval_seconds"] = int(match.group(1))

    if shutil.which("systemctl") is None:
        return status

    status["loaded"] = is_active()

    service = _show(SERVICE_NAME, "ExecMainStartTimestamp", "ExecMainStatus")
    if service.get("ExecMainStartTimestamp"):
        try:
            status["last_exit_status"] = int(service.get("ExecMainStatus", ""))
        except ValueError:
            pass

    return status
