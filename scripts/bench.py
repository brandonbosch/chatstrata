"""Baseline timings for the Python app, to compare the Go rewrite against.

    # Your real transcripts (copied to a temp dir first; never modified):
    python scripts/bench.py --source claude_code
    python scripts/bench.py --source codex_cli --path ~/.codex/sessions

    # A synthetic Claude Code corpus, for a reproducible comparison:
    python scripts/bench.py --synthetic 500

    # The same measurements for another build, e.g. the Go binary:
    python scripts/bench.py --synthetic 500 --binary bin/chatstrata

Every measurement runs the `chatstrata` CLI as a fresh process, the way users
and agents call it, so interpreter start-up and imports are included. Results
are printed as a table and written as JSON (--output).

Timings are with a warm OS file cache; dropping caches needs root and is
platform-specific, so "cold" here means "first run in a new process".
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import random
import shutil
import statistics
import subprocess
import sys
import tempfile
import time
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path

CHATSTRATA = [sys.executable, "-c", "from chatstrata.cli import cli; cli()"]
DEFAULT_PATHS = {
    "claude_code": "~/.claude/projects",
    "codex_cli": "~/.codex/sessions",
    "omp": "~/.omp/agent/sessions",
}
WORDS = (
    "refactor auth module database migration test failing parser index query cache "
    "deploy config schema adapter session token retry timeout error handler build "
    "release lint format docs endpoint request response json yaml golang python duckdb "
    "transcript archive search embedding sync relay device key encrypt compress"
).split()


def _sentence(rng: random.Random, n: int) -> str:
    return " ".join(rng.choice(WORDS) for _ in range(n)).capitalize() + "."


def make_synthetic(root: Path, sessions: int, turns: int, seed: int = 7) -> None:
    """Write Claude Code-shaped transcripts: user prompt, assistant with tool use,
    tool result, assistant answer, repeated `turns` times per session."""
    rng = random.Random(seed)
    start = datetime(2026, 1, 1, tzinfo=timezone.utc)
    for s in range(sessions):
        project = f"-Users-example-proj{s % 12}"
        cwd = f"/Users/example/proj{s % 12}"
        (root / project).mkdir(parents=True, exist_ok=True)
        clock = start + timedelta(hours=s * 3)
        parent = None
        lines = [{"type": "summary", "summary": _sentence(rng, 6)}]

        def event(kind: str, message: dict) -> dict:
            nonlocal parent, clock
            clock += timedelta(seconds=rng.randint(2, 90))
            ev = {
                "type": kind,
                "uuid": str(uuid.UUID(int=rng.getrandbits(128))),
                "parentUuid": parent,
                "timestamp": clock.isoformat().replace("+00:00", "Z"),
                "cwd": cwd,
                "message": message,
            }
            parent = ev["uuid"]
            return ev

        for t in range(turns):
            tool_id = f"toolu_{s}_{t}"
            lines.append(event("user", {"role": "user", "content": _sentence(rng, 25)}))
            lines.append(
                event(
                    "assistant",
                    {
                        "role": "assistant",
                        "model": "claude-opus-4-7",
                        "content": [
                            {"type": "text", "text": _sentence(rng, 40)},
                            {
                                "type": "tool_use",
                                "id": tool_id,
                                "name": rng.choice(["Read", "Edit", "Bash", "Grep"]),
                                "input": {"path": f"{cwd}/src/file{t}.py"},
                            },
                        ],
                    },
                )
            )
            lines.append(
                event(
                    "user",
                    {
                        "role": "user",
                        "content": [
                            {
                                "type": "tool_result",
                                "tool_use_id": tool_id,
                                "content": "\n".join(_sentence(rng, 12) for _ in range(20)),
                            }
                        ],
                    },
                )
            )
            lines.append(
                event(
                    "assistant",
                    {
                        "role": "assistant",
                        "model": "claude-opus-4-7",
                        "content": [
                            {"type": "thinking", "thinking": _sentence(rng, 30)},
                            {"type": "text", "text": _sentence(rng, 60)},
                        ],
                    },
                )
            )
        session_id = str(uuid.UUID(int=rng.getrandbits(128)))
        with (root / project / f"{session_id}.jsonl").open("w") as f:
            for line in lines:
                f.write(json.dumps(line) + "\n")


def _run(command: list[str], args: list[str], env: dict) -> tuple[float, float]:
    """Run the CLI once; return (seconds, peak RSS of that child in MiB)."""
    with tempfile.TemporaryFile() as stderr:
        started = time.perf_counter()
        proc = subprocess.Popen(command + args, env=env, stdout=subprocess.DEVNULL, stderr=stderr)
        # wait4 gives this child's own resource usage, unlike RUSAGE_CHILDREN.
        _, status, usage = os.wait4(proc.pid, 0)
        elapsed = time.perf_counter() - started
        proc.returncode = os.waitstatus_to_exitcode(status)
        if proc.returncode != 0:
            stderr.seek(0)
            raise RuntimeError(f"chatstrata {' '.join(args)} failed:\n{stderr.read().decode()}")
    # ru_maxrss is KiB on Linux, bytes on macOS.
    scale = 1024 * 1024 if sys.platform == "darwin" else 1024
    return elapsed, usage.ru_maxrss / scale


def _append_to_newest(root: Path, files: int) -> int:
    """Append a copy of each file's last line (with a fresh uuid) to the newest files."""
    candidates = sorted(root.rglob("*.jsonl"), key=lambda p: p.stat().st_mtime)[-files:]
    for path in candidates:
        last = path.read_text().rstrip("\n").splitlines()[-1]
        try:
            event = json.loads(last)
            event["uuid"] = str(uuid.uuid4())
            last = json.dumps(event)
        except (json.JSONDecodeError, TypeError):
            pass
        with path.open("a") as f:
            f.write(last + "\n")
        mtime = time.time() + 10
        os.utime(path, (mtime, mtime))
    return len(candidates)


def _dir_size(path: Path) -> int:
    return sum(p.stat().st_size for p in path.rglob("*") if p.is_file())


def bench(command: list[str], source: str, corpus: Path, repeat: int, search_term: str) -> dict:
    results: dict = {}
    with tempfile.TemporaryDirectory(prefix="chatstrata-bench-") as tmp:
        tmp_path = Path(tmp)
        work, db, home = tmp_path / "corpus", tmp_path / "archive.duckdb", tmp_path / "home"
        shutil.copytree(corpus, work)
        home.mkdir()
        env = dict(os.environ)
        if command == CHATSTRATA:
            # Keep the Python app away from the user's config. The Go binary
            # writes no config, and needs the real HOME to find DuckDB's
            # installed extensions.
            env.update(HOME=str(home), XDG_CONFIG_HOME=str(home / ".config"))
        ingest = ["ingest", source, "--path", str(work), "--db", str(db)]

        def measure(name: str, args: list[str], times: int = repeat) -> None:
            runs = [_run(command, args, env) for _ in range(times)]
            secs = [r[0] for r in runs]
            results[name] = {
                "first_s": round(secs[0], 4),
                "median_s": round(statistics.median(secs), 4),
                "stdev_s": round(statistics.stdev(secs), 4) if len(secs) > 1 else 0.0,
                "runs": len(secs),
                "peak_rss_mib": round(max(r[1] for r in runs), 1),
            }

        measure("startup (--help)", ["--help"])
        measure("full ingest", ingest, times=1)
        measure("incremental, nothing changed", ingest + ["--incremental"])
        appended = _append_to_newest(work, 5)
        measure(f"incremental, {appended} files appended", ingest + ["--incremental"], times=1)
        measure("reindex (FTS)", ["reindex", "--db", str(db)], times=1)
        measure("search", ["search", search_term, "--db", str(db)])
        measure("stats", ["stats", "--db", str(db)])
        measure(
            "query (tool counts)",
            [
                "query",
                "SELECT tool_name, count(*) FROM tool_calls GROUP BY 1 ORDER BY 2 DESC",
                "--db",
                str(db),
            ],
        )

        corpus_info = {
            "files": sum(1 for _ in work.rglob("*") if _.is_file()),
            "bytes": _dir_size(work),
            "db_bytes": db.stat().st_size,
        }
    return {"measurements": results, "corpus": corpus_info}


def environment(command: list[str]) -> dict:
    import duckdb

    venv = Path(sys.prefix)
    if command != CHATSTRATA:
        binary = Path(command[0])
        return {
            "date": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "platform": platform.platform(),
            "machine": platform.machine(),
            "cpus": os.cpu_count(),
            "binary": str(binary),
            "install_bytes": binary.stat().st_size,
        }
    return {
        "date": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "platform": platform.platform(),
        "machine": platform.machine(),
        "cpus": os.cpu_count(),
        "python": platform.python_version(),
        "duckdb": duckdb.__version__,
        "install_bytes": _dir_size(venv) if venv != Path(sys.base_prefix) else None,
    }


def main() -> None:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("--source", default="claude_code")
    parser.add_argument(
        "--path", type=Path, help="Corpus root (defaults to the source's usual location)."
    )
    parser.add_argument(
        "--synthetic",
        type=int,
        metavar="SESSIONS",
        help="Benchmark a generated Claude Code corpus instead.",
    )
    parser.add_argument("--turns", type=int, default=40, help="Turns per synthetic session.")
    parser.add_argument("--repeat", type=int, default=5)
    parser.add_argument("--search", default="migration", help="Search term.")
    parser.add_argument(
        "--binary",
        type=Path,
        help="Benchmark this chatstrata executable instead of the Python app.",
    )
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    command = [str(args.binary.resolve())] if args.binary else CHATSTRATA

    with tempfile.TemporaryDirectory(prefix="chatstrata-corpus-") as tmp:
        if args.synthetic:
            if args.source != "claude_code":
                parser.error("--synthetic only generates claude_code transcripts")
            corpus = Path(tmp)
            make_synthetic(corpus, args.synthetic, args.turns)
            label = f"synthetic claude_code: {args.synthetic} sessions x {args.turns} turns"
        else:
            corpus = (args.path or Path(DEFAULT_PATHS.get(args.source, ""))).expanduser()
            if not corpus.is_dir():
                parser.error(f"corpus directory not found: {corpus} (use --path)")
            label = f"{args.source}: {corpus}"
        result = {
            "corpus_label": label,
            "environment": environment(command),
            **bench(command, args.source, corpus, args.repeat, args.search),
        }

    corpus = result["corpus"]
    print(f"{label}  [{args.binary or 'python'}]")
    print(
        f"{corpus['files']} files, {corpus['bytes'] / 1e6:.1f} MB in, {corpus['db_bytes'] / 1e6:.1f} MB database\n"
    )
    print(f"{'measurement':38} {'first':>8} {'median':>8} {'stdev':>7} {'runs':>4} {'peak MiB':>9}")
    for name, m in result["measurements"].items():
        print(
            f"{name:38} {m['first_s']:8.3f} {m['median_s']:8.3f} {m['stdev_s']:7.3f} "
            f"{m['runs']:4d} {m['peak_rss_mib']:9.1f}"
        )
    if args.output:
        args.output.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
