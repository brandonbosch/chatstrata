# Connect an agent through MCP

The server is part of the Go binary. There is no Python MCP extra or separate
`chatstrata-mcp` executable to install. Collect an archive first:

```sh
chatstrata ingest --auto
chatstrata mcp config claude-code
chatstrata mcp config codex
chatstrata mcp config claude-desktop
```

Choose your client and apply its printed command/configuration. The command
prints instructions; it does not modify client configuration. For a custom
archive, add `--db /absolute/path/archive.duckdb`. Restart/reconnect the MCP
client afterward. The generated command uses the binary's absolute path.

The resulting configuration starts `chatstrata serve` over stdio. Tools:

- `get_schema`: table/column types, relationships, row counts, and examples.
- `query`: read-only DuckDB SQL, returning columns, rows, row_count, and a
  truncation indicator when needed.
- Resource `chatstrata://schema`: the same schema as `get_schema`.

Ask your agent: “Read the schema, then count conversations and messages by
source.” It can explore tool usage, projects, activity over time, and archived
text with normal SQL. Query results may be sent to the agent's model provider.

Database writes, external file/network reads, extension loading/installation,
and configuration changes are blocked. Queries time out after 30 seconds and
results are limited to 500 rows / 512 KB. The archive is opened and closed per
request so the collector can write between calls. A request may wait briefly
while a collection phase holds the writer lock.

HTTP transports are also available with `serve --transport streamable-http`
(`/mcp`) or `--transport sse` (`/sse`), bound to `127.0.0.1:8462` by default.
They do not supply authentication for a public service. Use stdio for normal
local agent integration; do not expose the archive at the public installer URL.
