# Features outside v2

Classifier/Jev labeling, PII sanitization, embeddings, and semantic search
are not included in chatstrata 2. Local chat archiving, keyword search, SQL,
and MCP analysis are available without them.

The compatible label and embedding tables remain in the schema, but the Go
application does not populate them or migrate old labels/embeddings. The
`python-final` tag preserves the historical implementation.
