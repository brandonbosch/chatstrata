# Privacy and data handling

The archive is local by default. Collection, search, SQL, and MCP do not call
a hosted API or upload transcripts. Release binaries bundle their search
extension. Source builds download it only after `reindex --install-fts`.

Local DuckDB files, observation logs, and original transcripts are plaintext.
chatstrata 2 does not automatically sanitize PII or credentials. OS disk
encryption and backups remain your responsibility. Logs and backups can retain
content even after the source tool removes its original transcript.

Connecting an agent exposes query results to that agent and potentially its
model provider. Select an appropriate archive and query only what the task
needs. MCP/CLI SQL is read-only and archive-scoped; external file reads and
network access are disabled.

Optional device sync encrypts segments before sending them to your configured
relay. The relay stores ciphertext; local copies remain plaintext. The
pairing code contains the space key: share it only with your own trusted
devices. Key rotation and device revocation are not included yet. See the
[sync security limits](https://github.com/brandonbosch/chatstrata/blob/main/docs/rewrite/sync.md#security-limits-v20).

Classifier/Jev labeling, automatic PII redaction, embeddings, and semantic
search are not part of this release. The `python-final` tag preserves the old
implementation for historical reference.
