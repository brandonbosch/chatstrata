"""ChatStrata MCP server.

A single-tool MCP server that exposes your conversation archive
via SQL queries against DuckDB.
"""

from __future__ import annotations

import json
import logging

import duckdb
from mcp.server.fastmcp import FastMCP

from chatstrata.core.db import get_default_db_path
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


def _open_readonly() -> duckdb.DuckDBPyConnection:
    """Open a read-only DuckDB connection with extensions loaded."""
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

    Full-text search (BM25):
        WHERE fts_main_content_blocks.match_bm25(cb.id, 'search terms') IS NOT NULL
        ORDER BY fts_main_content_blocks.match_bm25(cb.id, 'search terms') DESC

    Tips:
    - Use LEFT(cb.text, 500) to truncate long text content
    - tool_calls view is convenient for tool usage analysis
    - payload column is JSON -- use ->> for extraction
    - Only SELECT/WITH/DESCRIBE/SHOW/PRAGMA are allowed
    - Results limited to 500 rows / 512 KB
    """
    conn = _open_readonly()
    try:
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
            "WHERE table_schema = 'main' "
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

        return f"""# ChatStrata Database Schema

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
    args = parser.parse_args()

    if args.transport == "stdio":
        mcp.run(transport="stdio")
    elif args.transport == "streamable-http":
        mcp.run(transport="streamable-http", host=args.host, port=args.port)
    elif args.transport == "sse":
        mcp.run(transport="sse", host=args.host, port=args.port)


if __name__ == "__main__":
    main()
