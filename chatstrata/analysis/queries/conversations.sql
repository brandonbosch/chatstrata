SELECT
    c.title,
    s.id AS source,
    c.project,
    c.message_count,
    c.started_at,
    c.ended_at
FROM conversations c
JOIN sources s ON s.id = c.source_id
ORDER BY c.message_count {order}
LIMIT {limit}
