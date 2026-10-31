-- Example chatstrata queries. Use as: chatstrata query "$(cat path/to/query.sql)"
-- or paste into a DuckDB shell connected to your chatstrata.duckdb file.

-- ============================================================
-- 1. Activity over time
-- ============================================================

-- Messages per month, by source
SELECT
    s.id AS source,
    date_trunc('month', m.created_at) AS month,
    COUNT(*) AS messages
FROM messages m
JOIN conversations c ON c.id = m.conversation_id
JOIN sources s ON s.id = c.source_id
WHERE m.created_at IS NOT NULL
GROUP BY source, month
ORDER BY month DESC, source;

-- ============================================================
-- 2. Conversation length distribution
-- ============================================================

SELECT
    CASE
        WHEN message_count < 5 THEN 'very short (< 5)'
        WHEN message_count < 20 THEN 'short (5-19)'
        WHEN message_count < 50 THEN 'medium (20-49)'
        ELSE 'long (50+)'
    END AS length_bucket,
    COUNT(*) AS conversations
FROM conversations
GROUP BY length_bucket
ORDER BY conversations DESC;

-- ============================================================
-- 3. Every bash command via Claude Code, grouped by project
-- ============================================================

SELECT
    c.project,
    cb.payload->'input'->>'command' AS command,
    COUNT(*) AS times_run
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use'
  AND cb.tool_name = 'Bash'
  AND c.source_id = 'claude_code'
GROUP BY c.project, command
ORDER BY times_run DESC
LIMIT 50;

-- ============================================================
-- 4. Tool usage frequency
-- ============================================================

SELECT
    tool_name,
    COUNT(*) AS calls,
    COUNT(DISTINCT conversation_id) AS conversations
FROM tool_calls
GROUP BY tool_name
ORDER BY calls DESC;

-- ============================================================
-- 5. Word count of user prompts over time
-- ============================================================

-- Rough proxy for "how have I been prompting"
SELECT
    date_trunc('week', m.created_at) AS week,
    AVG(length(cb.text) - length(replace(cb.text, ' ', ''))) AS avg_word_count
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
WHERE m.role = 'user'
  AND cb.type = 'text'
  AND cb.text IS NOT NULL
GROUP BY week
ORDER BY week;

-- ============================================================
-- 6. Recent conversations matching a keyword
-- ============================================================

SELECT
    c.started_at,
    s.id AS source,
    c.project,
    c.title
FROM conversations c
JOIN sources s ON s.id = c.source_id
WHERE EXISTS (
    SELECT 1 FROM messages m
    JOIN content_blocks cb ON cb.message_id = m.id
    WHERE m.conversation_id = c.id
      AND cb.text ILIKE '%grandma%'  -- replace with your keyword
)
ORDER BY c.started_at DESC
LIMIT 20;

-- ============================================================
-- 7. Most-used models
-- ============================================================

SELECT model, COUNT(*) AS messages
FROM messages
WHERE model IS NOT NULL
GROUP BY model
ORDER BY messages DESC;

-- ============================================================
-- 8. Thinking blocks: how often does the model reason explicitly?
-- ============================================================

SELECT
    date_trunc('month', m.created_at) AS month,
    SUM(CASE WHEN cb.type = 'thinking' THEN 1 ELSE 0 END) AS thinking_blocks,
    COUNT(DISTINCT m.id) AS assistant_messages,
    ROUND(
        100.0 * SUM(CASE WHEN cb.type = 'thinking' THEN 1 ELSE 0 END)
              / NULLIF(COUNT(DISTINCT m.id), 0),
        1
    ) AS pct
FROM messages m
LEFT JOIN content_blocks cb ON cb.message_id = m.id
WHERE m.role = 'assistant'
GROUP BY month
ORDER BY month DESC;
