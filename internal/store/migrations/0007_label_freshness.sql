-- chatstrata schema v7: label freshness fingerprints and redaction provenance (ADR 0007).
--
-- Append-only label runs under chatstrata-go.labels/ are the source of truth;
-- these nullable columns preserve compatibility with v4 queries while allowing
-- incremental re-labeling of grown conversations and redaction auditability.

ALTER TABLE labels ADD COLUMN IF NOT EXISTS target_fingerprint VARCHAR;
ALTER TABLE label_runs ADD COLUMN IF NOT EXISTS redaction_version VARCHAR;
