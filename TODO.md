# Future work

The Go cutover is complete. The old Python release checklist is retired;
current build and packaging checks are in the [release procedure](docs/rewrite/release.md).
Python sources remain at `python-final`, and existing PyPI releases stay available.

## Additional sources

ChatGPT web exports and Cursor are not supported yet. New adapters need
synthetic fixtures and observation-log rebuild coverage. See the
[adapter guide](docs/adapter-guide.md).

## Compaction audit

Detect Claude Code and Codex compaction markers and preserve them as queryable
metadata or a dedicated view. Report whether raw and normalized history remain
available, whether a compact summary exists, and which nearby events were skipped.
Investigate post-compaction signals such as repeated discovery, contradicted
decisions, missing constraints, or degraded tool success.

## Features outside the current release

Classifier/Jev labeling, automatic PII sanitization, embeddings, and semantic
search are future Go work. Their historical Python implementation does not
imply support in the current binary.
