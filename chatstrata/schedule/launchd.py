"""macOS launchd integration for periodic chatstrata sync."""

from __future__ import annotations

import plistlib
import shutil
import subprocess
from pathlib import Path

LABEL = "com.chatstrata.sync"
PLIST_PATH = Path.home() / "Library" / "LaunchAgents" / f"{LABEL}.plist"
LOG_DIR = Path.home() / "Library" / "Logs" / "chatstrata"


def _find_chatstrata_binary() -> str:
    which = shutil.which("chatstrata")
    if which:
        return which
    venv_bin = Path(__file__).resolve().parents[1] / ".venv" / "bin" / "chatstrata"
    if venv_bin.exists():
        return str(venv_bin)
    raise FileNotFoundError(
        "Cannot find 'chatstrata' binary. Ensure it is installed and on PATH, "
        "or pass --binary to specify the path."
    )


def build_plist(*, interval_seconds: int, binary: str | None = None, no_embed: bool = False) -> dict:
    binary = binary or _find_chatstrata_binary()
    args = [binary, "ingest", "--auto"]
    if no_embed:
        args.append("--no-embed")

    return {
        "Label": LABEL,
        "ProgramArguments": args,
        "StartInterval": interval_seconds,
        "RunAtLoad": True,
        "StandardOutPath": str(LOG_DIR / "sync.log"),
        "StandardErrorPath": str(LOG_DIR / "sync.err"),
        "ProcessType": "Background",
    }


def install(*, interval_seconds: int, binary: str | None = None, no_embed: bool = False) -> Path:
    plist = build_plist(interval_seconds=interval_seconds, binary=binary, no_embed=no_embed)

    if is_loaded():
        uninstall()

    LOG_DIR.mkdir(parents=True, exist_ok=True)
    PLIST_PATH.parent.mkdir(parents=True, exist_ok=True)

    with open(PLIST_PATH, "wb") as f:
        plistlib.dump(plist, f)

    subprocess.run(["launchctl", "load", str(PLIST_PATH)], check=True)
    return PLIST_PATH


def uninstall() -> None:
    if PLIST_PATH.exists():
        subprocess.run(["launchctl", "unload", str(PLIST_PATH)], check=False)
        PLIST_PATH.unlink()


def is_loaded() -> bool:
    result = subprocess.run(
        ["launchctl", "list", LABEL],
        capture_output=True,
        text=True,
    )
    return result.returncode == 0


def get_status() -> dict:
    loaded = is_loaded()
    plist_exists = PLIST_PATH.exists()

    status: dict = {
        "installed": plist_exists,
        "loaded": loaded,
        "plist_path": str(PLIST_PATH),
        "log_dir": str(LOG_DIR),
    }

    if plist_exists:
        with open(PLIST_PATH, "rb") as f:
            plist = plistlib.load(f)
        status["interval_seconds"] = plist.get("StartInterval")
        status["binary"] = plist.get("ProgramArguments", [None])[0]
        status["no_embed"] = "--no-embed" in plist.get("ProgramArguments", [])

    if loaded:
        result = subprocess.run(
            ["launchctl", "list", LABEL],
            capture_output=True,
            text=True,
        )
        for line in result.stdout.strip().splitlines():
            if '"LastExitStatus"' in line or "LastExitStatus" in line:
                parts = line.strip().rstrip(";").split("=")
                if len(parts) == 2:
                    try:
                        status["last_exit_status"] = int(parts[1].strip())
                    except ValueError:
                        pass

    stdout_log = LOG_DIR / "sync.log"
    stderr_log = LOG_DIR / "sync.err"
    if stdout_log.exists():
        status["stdout_log"] = str(stdout_log)
    if stderr_log.exists():
        status["stderr_log"] = str(stderr_log)

    return status
