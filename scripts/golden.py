"""Golden output for the v2 rewrite: what the Python ingester produces, in a stable form.

    python scripts/golden.py generate        # rewrite spec/golden/expected/*.json
    python scripts/golden.py check           # fail if the Python app no longer matches them
    python scripts/golden.py dump <db.duckdb> [-o out.json]

`dump` works on any chatstrata database, so a real archive can be ingested by
both implementations and compared. See spec/README.md for the format.
"""

from __future__ import annotations

import argparse
import difflib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path

import duckdb

ROOT = Path(__file__).resolve().parent.parent
SPEC = ROOT / "spec" / "golden"
CHATSTRATA = [sys.executable, "-c", "from chatstrata.cli import cli; cli()"]


def _timestamp(value: datetime | None) -> str | None:
    """RFC 3339 in UTC with trailing fractional zeros trimmed (Go's RFC3339Nano)."""
    if value is None:
        return None
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    value = value.astimezone(timezone.utc)
    text = value.strftime("%Y-%m-%dT%H:%M:%S")
    if value.microsecond:
        text += f".{value.microsecond:06d}".rstrip("0")
    return text + "Z"


def _json_value(raw: str | None):
    return None if raw is None else json.loads(raw)


def _replace_paths(value, replacements: list[tuple[str, str]]):
    if isinstance(value, str):
        for old, new in replacements:
            value = value.replace(old, new)
        return value
    if isinstance(value, list):
        return [_replace_paths(v, replacements) for v in value]
    if isinstance(value, dict):
        return {k: _replace_paths(v, replacements) for k, v in value.items()}
    return value


def dump(db_path: Path, replacements: list[tuple[str, str]] | None = None) -> dict:
    """Canonical projection of a chatstrata database.

    Leaves out everything implementation-specific: internal ids, ingest
    timestamps, file mtimes and content hashes. Rows are keyed by source
    identity and ordered deterministically.
    """
    conn = duckdb.connect(str(db_path), read_only=True)
    conn.execute("SET TimeZone = 'UTC'")
    try:
        conversations = []
        conv_rows = conn.execute(
            """
            SELECT id, source_id, source_native_id, title, project, started_at,
                   ended_at, message_count, raw_path, metadata
            FROM conversations
            ORDER BY source_id, source_native_id
            """
        ).fetchall()
        for (
            conv_id,
            source_id,
            native_id,
            title,
            project,
            started_at,
            ended_at,
            message_count,
            raw_path,
            metadata,
        ) in conv_rows:
            msg_rows = conn.execute(
                """
                SELECT m.id, m.sequence_index, m.source_native_id, p.source_native_id,
                       m.role, m.model, m.created_at, m.metadata
                FROM messages m
                LEFT JOIN messages p ON p.id = m.parent_message_id
                WHERE m.conversation_id = ?
                ORDER BY m.sequence_index
                """,
                [conv_id],
            ).fetchall()
            messages = []
            for (
                msg_id,
                seq,
                msg_native_id,
                parent_native_id,
                role,
                model,
                created_at,
                msg_metadata,
            ) in msg_rows:
                blocks = [
                    {
                        "block_index": b[0],
                        "type": b[1],
                        "text": b[2],
                        "tool_name": b[3],
                        "tool_use_id": b[4],
                        "payload": _json_value(b[5]),
                    }
                    for b in conn.execute(
                        """
                        SELECT block_index, type, text, tool_name, tool_use_id, payload
                        FROM content_blocks WHERE message_id = ? ORDER BY block_index
                        """,
                        [msg_id],
                    ).fetchall()
                ]
                messages.append(
                    {
                        "sequence_index": seq,
                        "source_native_id": msg_native_id,
                        "parent_source_native_id": parent_native_id,
                        "role": role,
                        "model": model,
                        "created_at": _timestamp(created_at),
                        "metadata": _json_value(msg_metadata),
                        "blocks": blocks,
                    }
                )
            raw_events = [
                {"line_number": r[0], "raw_path": r[1], "payload": _json_value(r[2])}
                for r in conn.execute(
                    """
                    SELECT line_number, raw_path, payload FROM raw_events
                    WHERE source_id = ? AND source_native_conversation_id = ?
                    ORDER BY line_number, raw_path
                    """,
                    [source_id, native_id],
                ).fetchall()
            ]
            conversations.append(
                {
                    "source_id": source_id,
                    "source_native_id": native_id,
                    "title": title,
                    "project": project,
                    "started_at": _timestamp(started_at),
                    "ended_at": _timestamp(ended_at),
                    "message_count": message_count,
                    "raw_path": raw_path,
                    "metadata": _json_value(metadata),
                    "messages": messages,
                    "raw_events": raw_events,
                }
            )
        result = {"conversations": conversations}
    finally:
        conn.close()
    return _replace_paths(result, replacements or [])


def _materialize(case: dict, work: Path) -> None:
    """Copy a case's inputs into `work`, building any *.sql dump into a SQLite *.db."""
    shutil.copytree(SPEC / "inputs" / case["input"], work)
    for sql in work.rglob("*.sql"):
        conn = sqlite3.connect(sql.with_suffix(".db"))
        conn.executescript(sql.read_text())
        conn.close()
        sql.unlink()


def _apply_truncation(step: dict, work: Path, originals: dict[str, bytes], tick: int) -> None:
    truncate = step.get("truncate_lines", {})
    for rel, original in originals.items():
        target = work / rel
        content = original
        if rel in truncate:
            content = b"".join(original.splitlines(keepends=True)[: truncate[rel]])
        if target.read_bytes() != content:
            target.write_bytes(content)
            # Incremental ingest keys off mtime; make every change visible.
            os.utime(target, (1_800_000_000 + tick, 1_800_000_000 + tick))


def run_case(case: dict) -> dict:
    with tempfile.TemporaryDirectory(prefix="chatstrata-golden-") as tmp:
        tmp_path = Path(tmp)
        work, home, db = tmp_path / "input", tmp_path / "home", tmp_path / "archive.duckdb"
        home.mkdir()
        _materialize(case, work)
        truncated = {rel for step in case["steps"] for rel in step.get("truncate_lines", {})}
        originals = {rel: (work / rel).read_bytes() for rel in truncated}
        env = {
            **os.environ,
            "HOME": str(home),
            "XDG_DATA_HOME": str(home / ".local" / "share"),
            "XDG_CONFIG_HOME": str(home / ".config"),
            "HERMES_HOME": str(home / ".hermes"),
            "TZ": "UTC",
        }
        for tick, step in enumerate(case["steps"]):
            _apply_truncation(step, work, originals, tick)
            args = ["ingest", case["source"], "--path", str(work / case["path"]), "--db", str(db)]
            if step.get("incremental"):
                args.append("--incremental")
            proc = subprocess.run(CHATSTRATA + args, env=env, capture_output=True, text=True)
            if proc.returncode != 0:
                raise RuntimeError(f"{case['name']}: ingest failed\n{proc.stdout}\n{proc.stderr}")
        return dump(db, [(str(work), "$INPUT"), (str(home), "$HOME")])


def _render(data: dict) -> str:
    return json.dumps(data, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def _cases() -> list[dict]:
    return json.loads((SPEC / "cases.json").read_text())


def generate() -> None:
    out_dir = SPEC / "expected"
    out_dir.mkdir(exist_ok=True)
    for case in _cases():
        (out_dir / f"{case['name']}.json").write_text(_render(run_case(case)))
        print(f"wrote expected/{case['name']}.json")


def check() -> list[str]:
    """Return a unified diff per case whose current output differs from expected."""
    failures = []
    for case in _cases():
        expected_path = SPEC / "expected" / f"{case['name']}.json"
        expected = expected_path.read_text() if expected_path.exists() else ""
        actual = _render(run_case(case))
        if actual != expected:
            diff = difflib.unified_diff(
                expected.splitlines(keepends=True),
                actual.splitlines(keepends=True),
                f"expected/{case['name']}.json",
                "actual",
            )
            failures.append("".join(diff))
    return failures


def main() -> None:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("generate")
    sub.add_parser("check")
    dump_parser = sub.add_parser("dump")
    dump_parser.add_argument("db", type=Path)
    dump_parser.add_argument("-o", "--output", type=Path)
    args = parser.parse_args()

    if args.command == "generate":
        generate()
    elif args.command == "check":
        failures = check()
        for diff in failures:
            print(diff)
        sys.exit(1 if failures else 0)
    else:
        text = _render(dump(args.db))
        if args.output:
            args.output.write_text(text)
        else:
            sys.stdout.write(text)


if __name__ == "__main__":
    main()
