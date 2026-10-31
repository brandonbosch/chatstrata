# Your AI conversations, queryable by your agents

chatstrata archives Claude Code, Codex CLI, OpenCode, Oh My Pi, Hermes Agent,
and claude.ai exports in a local DuckDB database. Search across tools, run SQL,
or connect an agent through MCP. One Go binary; no Python runtime or API key.

```sh
curl -fsSL https://chatstrata.sprawlcommons.com/install.sh | sh
chatstrata ingest --auto
chatstrata schedule install
chatstrata mcp config codex
```

The installer supports Linux x86-64/arm64 and Apple-silicon macOS. It verifies
the release checksum and installs to `~/.local/bin` without sudo. `mcp config`
prints client setup instructions for you to apply.

[Get started](getting-started.md), [connect an agent](mcp.md), or
[see the sources](sources.md).

Your archive stays local by default. Local data is plaintext, and v2 does not
include automatic PII sanitization, classifier/Jev labels, embeddings, or semantic
search. [Privacy and data handling](privacy.md) explains the boundaries.
