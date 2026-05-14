SELECT
    model,
    COUNT(*) AS messages,
    COUNT(DISTINCT m.conversation_id) AS conversations
FROM messages m
WHERE model IS NOT NULL
GROUP BY model
ORDER BY messages DESC
