"""Analysis query helpers."""

from __future__ import annotations

from pathlib import Path

_QUERIES_DIR = Path(__file__).parent / "queries"


def load_query(name: str) -> str:
    """Load a .sql file from the queries directory by name (without extension)."""
    path = _QUERIES_DIR / f"{name}.sql"
    if not path.exists():
        raise FileNotFoundError(f"Query file not found: {path}")
    return path.read_text()


def build_source_filter(
    source: str | None, *, column: str = "c.source_id"
) -> tuple[str, list]:
    """Return a (sql_fragment, params) tuple for optional source filtering."""
    if source is None:
        return "", []
    return f"AND {column} = ?", [source]
