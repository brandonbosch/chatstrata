# Search and SQL

```sh
chatstrata search "auth migration"
chatstrata query "SELECT model, count(*) AS messages FROM messages GROUP BY model ORDER BY messages DESC" --json
chatstrata analyze activity --by month
chatstrata analyze tools
chatstrata analyze models
chatstrata analyze projects
chatstrata analyze conversations --longest 10
```

Release binaries bundle DuckDB's full-text extension. The index rebuilds in
batches; `search` also substring-matches new blocks awaiting indexing.
`chatstrata reindex` refreshes the index immediately. Source builds without
the bundled extension use substring search until the user explicitly runs
`reindex --install-fts`.

CLI queries and MCP share read-only rules: one SELECT, WITH, DESCRIBE, SHOW,
or a permitted metadata PRAGMA. External files/network, writes, extensions,
and configuration changes are disabled. Queries have a 30-second execution
timeout, 500-row limit, and 512 KB response budget. Large rows can be omitted;
use `LEFT(text, 500)` and narrower filters to request smaller results.

Agents should read [the schema](schema.md), join `messages` to `conversations`,
and use `content_blocks` for text or tool payloads. `tool_calls` is a convenient
view. For all newly collected text, `ILIKE` sees blocks that are not in the
BM25 index yet.

Use the CLI or MCP rather than leaving a DuckDB connection open in an analysis
scratchpad. Persistent read-only connections block the background writer.
