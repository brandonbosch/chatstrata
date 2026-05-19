# Security Policy

chatstrata is a local-first personal archive tool. It stores conversation data
on the user's machine and should not make network calls during ingestion,
querying, or redaction.

## Reporting a Vulnerability

Please report security issues privately through GitHub security advisories if
available, or by opening a minimal issue that does not include sensitive details.

Do not include private transcripts, API keys, database files, or other secrets in
public issues.

## Security Scope

Security-sensitive areas include:

- Accidental network calls during local operations.
- SQL surfaces that can mutate or exfiltrate local data unexpectedly.
- Redaction misses for common developer secrets.
- Packaging mistakes that include local data, caches, virtualenvs, or generated
  artifacts.
- Source adapters that parse untrusted exports unsafely.

Redaction is best-effort and is not a guarantee of anonymization. Users should
review redacted output before publishing it.

## Supported Versions

During the public alpha, only the latest released version is supported.
