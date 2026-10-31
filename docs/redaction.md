# PII sanitization

chatstrata 2 stores conversations as collected. It does not automatically redact
PII, credentials, or other sensitive text. Local archives and observation logs
are plaintext; optional relay sync encrypts segments before upload.

Choose which archive to expose through MCP and remember that query results may
be sent to an agent's model provider. Historical Python redaction code is
preserved at the `python-final` Git tag. Sanitization in Go is future work.
