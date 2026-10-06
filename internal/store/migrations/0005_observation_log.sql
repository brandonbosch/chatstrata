-- chatstrata schema v5 (Go only): index of the observation log (ADR 0005).
--
-- The log on disk is the source of truth; these tables are derived from it
-- and rebuilt by `chatstrata rebuild`. The Python implementation does not
-- know them and ignores them.

-- One row per observation, pointing at its record in a segment file.
CREATE TABLE IF NOT EXISTS observations (
    device_id VARCHAR NOT NULL,
    device_seq UBIGINT NOT NULL,
    source_id VARCHAR NOT NULL,
    scope VARCHAR NOT NULL,
    locator VARCHAR NOT NULL,         -- source-native conversation id
    kind VARCHAR NOT NULL,            -- append | snapshot | tombstone
    byte_offset BIGINT NOT NULL,
    length BIGINT NOT NULL,
    content_sha256 VARCHAR,
    observed_at TIMESTAMPTZ,          -- device clock, informational only
    path VARCHAR,                     -- file location on the observing device
    project_hint VARCHAR,
    legacy BOOLEAN NOT NULL DEFAULT false,
    segment VARCHAR NOT NULL,         -- relative to the log root
    position BIGINT NOT NULL,         -- byte offset of the record in the segment
    PRIMARY KEY (device_id, device_seq)
);

CREATE INDEX IF NOT EXISTS idx_observations_key ON observations(source_id, scope, locator);

-- Segment files already indexed, so catch-up only reads new ones.
CREATE TABLE IF NOT EXISTS indexed_segments (
    segment VARCHAR PRIMARY KEY
);

-- What this device last logged for each conversation, so the collector only
-- records what changed.
CREATE TABLE IF NOT EXISTS collector_state (
    source_id VARCHAR NOT NULL,
    scope VARCHAR NOT NULL,
    locator VARCHAR NOT NULL,
    length BIGINT NOT NULL,
    content_sha256 VARCHAR NOT NULL,
    mtime DOUBLE,
    PRIMARY KEY (source_id, scope, locator)
);

-- Conversations whose devices saw different continuations: one row per branch,
-- keyed by the branch's first message.
CREATE TABLE IF NOT EXISTS divergences (
    conversation_id VARCHAR NOT NULL,
    fork_after VARCHAR,               -- source-native id of the last shared message
    branch_message VARCHAR NOT NULL,  -- source-native id of the branch's first message
    devices JSON NOT NULL,            -- devices that observed this branch
    PRIMARY KEY (conversation_id, branch_message)
);
