-- chatstrata schema v4: labels produced by external classifiers (e.g. TypeSafe Jev)
--
-- A label is one typed answer to one question about one target (a tool call,
-- a message, or a conversation). Questions are grouped into "packs"; the
-- pack_version is a hash of the pack's questions and state builder, so
-- editing a pack invalidates its old labels instead of silently mixing them.

CREATE TABLE IF NOT EXISTS label_runs (
    id VARCHAR PRIMARY KEY,
    pack VARCHAR NOT NULL,
    pack_version VARCHAR NOT NULL,
    backend VARCHAR NOT NULL,         -- e.g. "typesafe"
    model VARCHAR,                    -- versioned model id that answered (e.g. jev-1.13.0)
    started_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    finished_at TIMESTAMPTZ,
    targets INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS labels (
    pack VARCHAR NOT NULL,
    target_kind VARCHAR NOT NULL,     -- tool_call | message | conversation
    target_id VARCHAR NOT NULL,       -- content_blocks.id | messages.id | conversations.id
    question VARCHAR NOT NULL,        -- question id within the pack
    answer_type VARCHAR NOT NULL,     -- noul | choice | score
    value DOUBLE,                     -- noul probability or score expectation
    choice VARCHAR,                   -- chosen option for choice answers
    confidence DOUBLE,                -- choice/score confidence; NULL for noul
    probabilities JSON,               -- full distribution for choice/score
    pack_version VARCHAR NOT NULL,
    model VARCHAR,
    run_id VARCHAR,
    created_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    PRIMARY KEY (pack, target_id, question)
);

CREATE INDEX IF NOT EXISTS idx_labels_target ON labels(target_id);
CREATE INDEX IF NOT EXISTS idx_labels_pack_question ON labels(pack, question);
