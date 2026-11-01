# Security policy

chatstrata stores AI conversations locally. Normal collection, search, SQL,
and MCP make no network requests. Optional device sync sends encrypted
observation-log segments to an explicitly configured relay.

## Report a vulnerability

Report security issues privately through GitHub security advisories when
available. Otherwise, open a minimal issue without sensitive details.
Never include private transcripts, API keys, pairing codes, or database files.

## Security scope

- SQL access must remain read-only and restricted to the archive.
- Source adapters parse untrusted local transcripts and exports.
- Releases must exclude private data, credentials, and local build artifacts.
- Device sync must protect segment contents and pairing keys.

Local transcripts, observation logs, and DuckDB files are plaintext. Go v2
has no automatic redaction or anonymization. Agent providers may receive MCP
query results. HTTP MCP transports do not provide public-service authentication;
use stdio for local clients. See the
[privacy guide](docs-site/docs/privacy.md) and
[sync security limits](docs/rewrite/sync.md#security-limits-v20).

## Supported versions

Only the latest Go release is supported. The Python application and PyPI
publishing are retired; historical source is preserved at `python-final`.
