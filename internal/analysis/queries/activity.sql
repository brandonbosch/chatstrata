SELECT
    s.id AS source,
    date_trunc('{granularity}', m.created_at) AS period,
    COUNT(*) AS messages,
    COUNT(DISTINCT m.conversation_id) AS conversations
FROM messages m
JOIN conversations c ON c.id = m.conversation_id
JOIN sources s ON s.id = c.source_id
WHERE m.created_at IS NOT NULL
  {source_filter}
GROUP BY source, period
ORDER BY period DESC, source
