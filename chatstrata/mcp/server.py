"""ChatStrata MCP server.

A single-tool MCP server that exposes your conversation archive
via SQL queries against DuckDB.
"""

from __future__ import annotations

import json
import logging
import os

import duckdb
from mcp.server.fastmcp import FastMCP

from chatstrata.core.db import get_default_db_path
from chatstrata.label.filter import FilterRule, apply_filter, check_query, parse_rules
from chatstrata.mcp.safety import execute_safe

logger = logging.getLogger(__name__)

EXAMPLE_QUERIES = """\
-- Recent conversations
SELECT title, source_id, started_at, message_count
FROM conversations ORDER BY started_at DESC LIMIT 10;

-- Tool usage frequency
SELECT tool_name, COUNT(*) AS calls, COUNT(DISTINCT conversation_id) AS conversations
FROM tool_calls GROUP BY tool_name ORDER BY calls DESC;

-- Bash commands by project (Claude Code)
SELECT c.project, cb.payload->'input'->>'command' AS command, COUNT(*) AS times_run
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use' AND cb.tool_name = 'Bash' AND c.source_id = 'claude_code'
GROUP BY c.project, command ORDER BY times_run DESC LIMIT 30;

-- Full-text search (BM25) for a topic
SELECT c.title, c.started_at, cb.text
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE fts_main_content_blocks.match_bm25(cb.id, 'your search term') IS NOT NULL
ORDER BY fts_main_content_blocks.match_bm25(cb.id, 'your search term') DESC
LIMIT 10;

-- Messages per month by source
SELECT s.id AS source, date_trunc('month', m.created_at) AS month, COUNT(*) AS messages
FROM messages m
JOIN conversations c ON c.id = m.conversation_id
JOIN sources s ON s.id = c.source_id
WHERE m.created_at IS NOT NULL
GROUP BY source, month ORDER BY month DESC, source;

-- Most-used models
SELECT model, COUNT(*) AS messages FROM messages
WHERE model IS NOT NULL GROUP BY model ORDER BY messages DESC;

-- Conversation length distribution
SELECT
    CASE
        WHEN message_count < 5 THEN 'very short (< 5)'
        WHEN message_count < 20 THEN 'short (5-19)'
        WHEN message_count < 50 THEN 'medium (20-49)'
        ELSE 'long (50+)'
    END AS length_bucket,
    COUNT(*) AS conversations
FROM conversations GROUP BY length_bucket ORDER BY conversations DESC;

-- Per-project conversation counts
SELECT c.project, COUNT(*) AS conversations, SUM(c.message_count) AS total_messages
FROM conversations c WHERE c.project IS NOT NULL
GROUP BY c.project ORDER BY conversations DESC;
"""

mcp = FastMCP(
    "ChatStrata",
    instructions=(
        "You have access to a personal conversation archive stored in DuckDB. "
        "Use the `query` tool to run read-only SQL against it. "
        "Read the chatstrata://schema resource first to understand the tables, "
        "column types, row counts, and relationships."
    ),
    stateless_http=True,
    json_response=True,
)


FILTER_ENV = "CHATSTRATA_MCP_FILTER"

# Set by --filter; otherwise read from $CHATSTRATA_MCP_FILTER on every call.
_filter_rules: list[FilterRule] | None = None


def set_filter(rules: list[FilterRule] | None) -> None:
    """Configure the content filter (None: use $CHATSTRATA_MCP_FILTER)."""
    global _filter_rules
    _filter_rules = rules


def _active_filter() -> list[FilterRule]:
    if _filter_rules is not None:
        return _filter_rules
    return parse_rules(os.environ.get(FILTER_ENV))


def _open_readonly() -> duckdb.DuckDBPyConnection:
    """Open a read-only DuckDB connection with extensions loaded.

    With a content filter configured, the archive tables are shadowed by
    filtered views and file access is switched off, so queries cannot reach
    raw transcripts on disk. If the filter cannot be applied this raises
    instead of returning an unfiltered connection.
    """
    db_path = get_default_db_path()
    if not db_path.exists():
        raise FileNotFoundError(
            f"ChatStrata database not found at {db_path}. "
            "Run `chatstrata ingest <source>` first."
        )
    conn = duckdb.connect(str(db_path), read_only=True)
    try:
        conn.execute("LOAD fts")
    except (duckdb.IOException, duckdb.CatalogException):
        pass
    try:
        conn.execute("LOAD vss")
    except (duckdb.IOException, duckdb.CatalogException):
        pass
    rules = _active_filter()
    if rules:
        try:
            apply_filter(conn, rules)
            # One-way: cannot be re-enabled for the life of the connection.
            conn.execute("SET enable_external_access = false")
        except BaseException:
            conn.close()
            raise
    return conn


@mcp.tool()
def query(sql: str) -> str:
    """Run a read-only SQL query against the ChatStrata conversation archive.

    The database contains conversations from Claude Code, claude.ai, ChatGPT,
    Codex CLI, OpenCode, and other AI providers, normalized into a common schema.

    Key tables:
    - conversations: id, source_id, title, project, started_at, ended_at, message_count
    - messages: id, conversation_id, role (user/assistant/system/tool), model, created_at
    - content_blocks: id, message_id, type (text/tool_use/tool_result/thinking), text, tool_name, payload (JSON)
    - tool_calls (VIEW): call_id, tool_name, input, conversation_id, project, created_at
    - sources: id, name
    - attachments: id, message_id, filename, mime_type
    - labels: pack, target_kind, target_id, question, answer_type, value, choice,
      confidence, probabilities (JSON), model -- classifier answers from
      `chatstrata label`; target_id joins to content_blocks.id (tool_call)
      or messages.id (message)

    Full-text search (BM25):
        WHERE fts_main_content_blocks.match_bm25(cb.id, 'search terms') IS NOT NULL
        ORDER BY fts_main_content_blocks.match_bm25(cb.id, 'search terms') DESC

    Tips:
    - Use LEFT(cb.text, 500) to truncate long text content
    - tool_calls view is convenient for tool usage analysis
    - payload column is JSON -- use ->> for extraction
    - Only SELECT/WITH/DESCRIBE/SHOW/PRAGMA are allowed
    - Results limited to 500 rows / 512 KB
    - If a content filter is on, some text shows as "[hidden by chatstrata
      filter: ...]" and its payload is NULL; that content is unavailable
    """
    try:
        conn = _open_readonly()
    except (ValueError, FileNotFoundError) as exc:  # FilterError is a ValueError
        return json.dumps({"error": str(exc)}, indent=2)
    except duckdb.Error as exc:
        return json.dumps({"error": f"Content filter could not be applied: {exc}"}, indent=2)
    try:
        if _active_filter():
            check_query(sql, conn.execute("SELECT current_database()").fetchone()[0])
        cols, rows, truncated = execute_safe(conn, sql)
        result: dict = {
            "columns": cols,
            "rows": [dict(zip(cols, row)) for row in rows],
            "row_count": len(rows),
        }
        if truncated:
            result["truncated"] = True
            result["note"] = "Results were truncated. Add LIMIT or narrow your WHERE clause."
        return json.dumps(result, default=str, indent=2)
    except (ValueError, TimeoutError) as exc:
        return json.dumps({"error": str(exc)}, indent=2)
    except duckdb.Error as exc:
        return json.dumps({"error": f"DuckDB error: {exc}"}, indent=2)
    finally:
        conn.close()


@mcp.resource("chatstrata://schema")
def get_schema() -> str:
    """Complete schema of the ChatStrata conversation archive.

    Includes table definitions, column types, row counts, relationships,
    and example queries to help write effective SQL.
    """
    conn = _open_readonly()
    try:
        tables_and_views = conn.execute(
            "SELECT table_name, table_type "
            "FROM information_schema.tables "
            "WHERE table_schema = 'main' AND table_catalog = current_database() "
            "ORDER BY table_type, table_name"
        ).fetchall()

        schema_parts: list[str] = []
        stats_parts: list[str] = []

        for table_name, table_type in tables_and_views:
            label = "VIEW" if table_type == "VIEW" else "TABLE"
            cols = conn.execute(
                "SELECT column_name, data_type, is_nullable "
                "FROM information_schema.columns "
                "WHERE table_name = ? ORDER BY ordinal_position",
                [table_name],
            ).fetchall()
            col_lines = "\n".join(
                f"    {name:30} {dtype}{' (nullable)' if nullable == 'YES' else ''}"
                for name, dtype, nullable in cols
            )
            schema_parts.append(f"{label} {table_name}:\n{col_lines}")

            try:
                count = conn.execute(f'SELECT COUNT(*) FROM "{table_name}"').fetchone()[0]
                stats_parts.append(f"  {table_name}: {count:,} rows")
            except duckdb.Error:
                stats_parts.append(f"  {table_name}: (unable to count)")

        schema_text = "\n\n".join(schema_parts)
        stats_text = "\n".join(stats_parts)
        rules = _active_filter()
        filter_text = (
            "\n## Content Filter\n"
            f"On ({', '.join(map(str, rules))}). Flagged and not-yet-labelled content "
            "shows as '[hidden by chatstrata filter: <reason>]' with NULL payload; "
            "titles and metadata of affected conversations are hidden too. Refer "
            "to tables unqualified (content_blocks, not <db>.main.content_blocks).\n"
            if rules
            else ""
        )

        return f"""# ChatStrata Database Schema
{filter_text}
{schema_text}

## Row Counts
{stats_text}

## Key Relationships
- conversations.source_id -> sources.id
- messages.conversation_id -> conversations.id
- content_blocks.message_id -> messages.id
- attachments.message_id -> messages.id
- tool_calls is a VIEW over content_blocks WHERE type = 'tool_use'

## Full-Text Search (BM25)
Use: fts_main_content_blocks.match_bm25(cb.id, 'your search terms')
Returns a float score (higher = more relevant), NULL for non-matches.
Filter with: WHERE fts_main_content_blocks.match_bm25(cb.id, 'term') IS NOT NULL
Order by score DESC for relevance ranking.

## Example Queries
{EXAMPLE_QUERIES}
"""
    finally:
        conn.close()


def main() -> None:
    """Entry point for chatstrata-mcp script and `chatstrata serve`."""
    import argparse

    parser = argparse.ArgumentParser(description="ChatStrata MCP Server")
    parser.add_argument(
        "--transport",
        choices=["stdio", "streamable-http", "sse"],
        default="stdio",
        help="Transport protocol (default: stdio)",
    )
    parser.add_argument("--host", default="127.0.0.1", help="Host for HTTP transports")
    parser.add_argument("--port", type=int, default=8462, help="Port for HTTP transports")
    parser.add_argument(
        "--filter",
        action="append",
        default=None,
        metavar="RULE",
        help=f"Hide content by label, e.g. cyber:security_related>0.3 (repeatable; "
        f"default: ${FILTER_ENV})",
    )
    args = parser.parse_args()
    if args.filter:
        set_filter(parse_rules(",".join(args.filter)))

    if args.transport == "stdio":
        mcp.run(transport="stdio")
    elif args.transport == "streamable-http":
        mcp.run(transport="streamable-http", host=args.host, port=args.port)
    elif args.transport == "sse":
        mcp.run(transport="sse", host=args.host, port=args.port)


if __name__ == "__main__":
    main()
