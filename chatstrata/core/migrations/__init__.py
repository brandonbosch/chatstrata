"""Schema migration framework for chatstrata."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

_DIR = Path(__file__).parent


@dataclass(frozen=True)
class Migration:
    version: int
    description: str
    sql: str


def _load(filename: str) -> str:
    return (_DIR / filename).read_text()


MIGRATIONS: list[Migration] = [
    Migration(version=1, description="Initial schema", sql=_load("0001_initial.sql")),
    Migration(version=2, description="Full-text search index", sql=_load("0002_fts_index.sql")),
]

LATEST_VERSION = MIGRATIONS[-1].version
