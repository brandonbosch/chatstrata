-- chatstrata schema v6: archive redaction overlay (ADR 0008).
--
-- The append-only sidecar under chatstrata-go.redactions/ is the source of
-- truth for redaction records. These tables mirror it for SQL audit and make
-- the projection cheap to rebuild. Redacted observations are marked so the
-- collector applies the policy to future source bytes instead of logging the
-- original plaintext again.

ALTER TABLE observations ADD COLUMN IF NOT EXISTS redacted BOOLEAN DEFAULT false;
UPDATE observations SET redacted = false WHERE redacted IS NULL;

CREATE TABLE IF NOT EXISTS redaction_runs (
    run_id VARCHAR PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL,
    redaction_version VARCHAR NOT NULL,
    records INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS redactions (
    run_id VARCHAR NOT NULL,
    target_kind VARCHAR NOT NULL,     -- content_blocks.text | content_blocks.payload | raw_events.payload | conversations.title
    target_id VARCHAR NOT NULL,
    input_sha256 VARCHAR NOT NULL,    -- hash of the text the spans were found in
    spans JSON NOT NULL,              -- [{"start","end","rule_id","category","priority"}]
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (run_id, target_kind, target_id)
);

CREATE INDEX IF NOT EXISTS idx_redactions_target ON redactions(target_kind, target_id);
