SELECT
    tool_name,
    COUNT(*) AS calls,
    COUNT(DISTINCT conversation_id) AS conversations
FROM tool_calls
WHERE tool_name IS NOT NULL
  {source_filter}
GROUP BY tool_name
ORDER BY calls DESC
