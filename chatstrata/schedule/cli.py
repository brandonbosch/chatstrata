"""CLI commands for managing scheduled background sync."""

from __future__ import annotations

import platform
import sys

import click


def _parse_interval(value: str) -> int:
    value = value.strip().lower()
    if value.endswith("m"):
        return int(value[:-1]) * 60
    if value.endswith("h"):
        return int(value[:-1]) * 3600
    if value.endswith("s"):
        return int(value[:-1])
    return int(value) * 60


@click.group()
def schedule() -> None:
    """Manage automatic background sync."""


@schedule.command()
@click.option(
    "--interval",
    default="15m",
    show_default=True,
    help="Sync interval (e.g. 15m, 1h, 900s).",
)
@click.option(
    "--binary",
    default=None,
    help="Path to the chatstrata binary (auto-detected by default).",
)
@click.option(
    "--no-embed",
    is_flag=True,
    help="Skip embedding generation during scheduled sync.",
)
def install(interval: str, binary: str | None, no_embed: bool) -> None:
    """Install automatic background sync.

    \b
    Examples:
        chatstrata schedule install
        chatstrata schedule install --interval 30m
        chatstrata schedule install --interval 1h --no-embed
    """
    system = platform.system()

    if system == "Darwin":
        from chatstrata.schedule.launchd import install as launchd_install

        interval_seconds = _parse_interval(interval)
        try:
            plist_path = launchd_install(
                interval_seconds=interval_seconds,
                binary=binary,
                no_embed=no_embed,
            )
        except FileNotFoundError as exc:
            click.echo(f"Error: {exc}", err=True)
            sys.exit(1)

        minutes = interval_seconds // 60
        click.echo(f"Installed launchd agent (every {minutes}m).")
        click.echo(f"  Plist: {plist_path}")
        click.echo(f"  Logs:  ~/Library/Logs/chatstrata/")
        click.echo()
        click.echo("Sync will run automatically in the background.")
        click.echo("Use `chatstrata schedule status` to check, `chatstrata schedule uninstall` to remove.")

    elif system == "Linux":
        click.echo("Linux (systemd) scheduling is not yet implemented.", err=True)
        click.echo("Run `chatstrata ingest --auto` via cron as a workaround:", err=True)
        click.echo(f"  */15 * * * * {sys.executable} -m chatstrata ingest --auto", err=True)
        sys.exit(1)

    else:
        click.echo(f"Unsupported platform: {system}", err=True)
        sys.exit(1)


@schedule.command()
def uninstall() -> None:
    """Remove background sync."""
    system = platform.system()

    if system == "Darwin":
        from chatstrata.schedule.launchd import uninstall as launchd_uninstall
        from chatstrata.schedule.launchd import PLIST_PATH

        if not PLIST_PATH.exists():
            click.echo("No scheduled sync is installed.")
            return
        launchd_uninstall()
        click.echo("Background sync removed.")

    elif system == "Linux":
        click.echo("Linux (systemd) scheduling is not yet implemented.", err=True)
        sys.exit(1)

    else:
        click.echo(f"Unsupported platform: {system}", err=True)
        sys.exit(1)


@schedule.command()
def status() -> None:
    """Show background sync status."""
    system = platform.system()

    if system == "Darwin":
        from chatstrata.schedule.launchd import get_status

        info = get_status()
        if not info["installed"]:
            click.echo("No scheduled sync installed.")
            click.echo("Run `chatstrata schedule install` to set up automatic background sync.")
            return

        state = "active" if info["loaded"] else "installed but not loaded"
        click.echo(f"Status:   {state}")
        if info.get("interval_seconds"):
            click.echo(f"Interval: {info['interval_seconds'] // 60}m")
        if info.get("binary"):
            click.echo(f"Binary:   {info['binary']}")
        if info.get("no_embed"):
            click.echo("Embed:    disabled")
        if "last_exit_status" in info:
            exit_code = info["last_exit_status"]
            label = "OK" if exit_code == 0 else f"exit {exit_code}"
            click.echo(f"Last run: {label}")
        click.echo(f"Plist:    {info['plist_path']}")
        if info.get("stdout_log"):
            click.echo(f"Log:      {info['stdout_log']}")

    elif system == "Linux":
        click.echo("Linux (systemd) scheduling is not yet implemented.", err=True)
        sys.exit(1)

    else:
        click.echo(f"Unsupported platform: {system}", err=True)
        sys.exit(1)
