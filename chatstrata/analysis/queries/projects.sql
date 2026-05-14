SELECT
    c.project,
    COUNT(*) AS conversations,
    SUM(c.message_count) AS total_messages,
    MIN(c.started_at) AS earliest,
    MAX(c.ended_at) AS latest
FROM conversations c
WHERE c.source_id = 'claude_code'
  AND c.project IS NOT NULL
GROUP BY c.project
ORDER BY conversations DESC
