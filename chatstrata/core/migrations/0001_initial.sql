-- chatstrata schema v1
--
-- Design notes:
--   - All timestamps are TIMESTAMPTZ, normalized to UTC at ingest time.
--   - Every normalized table has a source_id pointing back to `sources` so
--     we can always trace data to its origin.
--   - `raw_events` mirrors source data line-for-line, enabling re-parsing
--     without re-ingesting when normalization logic changes.
--   - content_blocks is the workhorse: anything that can appear inside a
--     message (text, tool_use, tool_result, thinking, image, attachment)
--     is a row here with a `type` discriminator.
--   - schema_version is tracked in `meta` for migrations.

CREATE TABLE IF NOT EXISTS meta (
    key VARCHAR PRIMARY KEY,
    value VARCHAR NOT NULL
);

CREATE TABLE IF NOT EXISTS sources (
    id VARCHAR PRIMARY KEY,           -- e.g. "claude_code", "chatgpt_export"
    name VARCHAR NOT NULL,            -- display name
    adapter_version VARCHAR,          -- version of the adapter that ingested
    first_seen TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    last_ingested TIMESTAMPTZ,
    config JSON                       -- adapter-specific config snapshot
);

CREATE TABLE IF NOT EXISTS conversations (
    id VARCHAR PRIMARY KEY,           -- chatstrata-internal id (uuid)
    source_id VARCHAR NOT NULL REFERENCES sources(id),
    source_native_id VARCHAR NOT NULL, -- the id assigned by the source
    title VARCHAR,
    project VARCHAR,                  -- workspace/project/cwd if applicable
    started_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    message_count INTEGER NOT NULL DEFAULT 0,
    content_hash VARCHAR,             -- sha256 of concatenated content, for dedup
    raw_path VARCHAR,                 -- where the original data lives on disk
    metadata JSON,                    -- source-specific extra fields
    ingested_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    UNIQUE (source_id, source_native_id)
);

CREATE INDEX IF NOT EXISTS idx_conversations_started ON conversations(started_at);
CREATE INDEX IF NOT EXISTS idx_conversations_project ON conversations(project);

CREATE TABLE IF NOT EXISTS messages (
    id VARCHAR PRIMARY KEY,           -- chatstrata-internal id
    conversation_id VARCHAR NOT NULL REFERENCES conversations(id),
    source_native_id VARCHAR,         -- id from source if available
    parent_message_id VARCHAR,        -- for tree-shaped histories (e.g. ChatGPT)
    role VARCHAR NOT NULL,            -- user, assistant, system, tool
    model VARCHAR,                    -- model name when applicable
    created_at TIMESTAMPTZ,
    sequence_index INTEGER NOT NULL,  -- order within conversation
    metadata JSON,
    UNIQUE (conversation_id, sequence_index)
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at);
CREATE INDEX IF NOT EXISTS idx_messages_role ON messages(role);

CREATE TABLE IF NOT EXISTS content_blocks (
    id VARCHAR PRIMARY KEY,
    message_id VARCHAR NOT NULL REFERENCES messages(id),
    block_index INTEGER NOT NULL,     -- order within the message
    type VARCHAR NOT NULL,            -- text | tool_use | tool_result | thinking | image | attachment
    text TEXT,                        -- populated for text/thinking
    tool_name VARCHAR,                -- populated for tool_use
    tool_use_id VARCHAR,              -- ties tool_use to tool_result
    payload JSON,                     -- type-specific structured data
    UNIQUE (message_id, block_index)
);

CREATE INDEX IF NOT EXISTS idx_content_message ON content_blocks(message_id);
CREATE INDEX IF NOT EXISTS idx_content_type ON content_blocks(type);
CREATE INDEX IF NOT EXISTS idx_content_tool_name ON content_blocks(tool_name);

-- Denormalized convenience view: every tool call as a row
CREATE VIEW IF NOT EXISTS tool_calls AS
SELECT
    cb.id AS call_id,
    cb.tool_use_id,
    cb.tool_name,
    cb.payload AS input,
    m.id AS message_id,
    m.conversation_id,
    m.created_at,
    c.project,
    c.source_id
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use';

CREATE TABLE IF NOT EXISTS attachments (
    id VARCHAR PRIMARY KEY,
    message_id VARCHAR NOT NULL REFERENCES messages(id),
    filename VARCHAR,
    mime_type VARCHAR,
    storage_path VARCHAR,             -- where chatstrata stored a copy if any
    source_url VARCHAR,               -- original location if applicable
    content_hash VARCHAR,             -- sha256
    size_bytes BIGINT,
    metadata JSON
);

-- Raw events: source data preserved line-for-line for re-parsing
CREATE TABLE IF NOT EXISTS raw_events (
    id VARCHAR PRIMARY KEY,           -- chatstrata-internal id
    source_id VARCHAR NOT NULL REFERENCES sources(id),
    source_native_conversation_id VARCHAR,
    raw_path VARCHAR,                 -- the file this came from
    line_number INTEGER,              -- line within the file if applicable
    payload JSON NOT NULL,            -- the exact source record
    ingested_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp
);

CREATE INDEX IF NOT EXISTS idx_raw_source_conv ON raw_events(source_id, source_native_conversation_id);

-- Embeddings table is reserved for future use. Empty for now.
-- Generation is lazy / opt-in, not part of standard ingest.
CREATE TABLE IF NOT EXISTS message_embeddings (
    message_id VARCHAR NOT NULL REFERENCES messages(id),
    model VARCHAR NOT NULL,
    vector FLOAT[],
    created_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    PRIMARY KEY (message_id, model)
);
