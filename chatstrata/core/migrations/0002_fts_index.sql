-- chatstrata schema v2: full-text search index on content_blocks.text
--
-- The FTS extension creates internal tables (fts_main_content_blocks_*)
-- that store the inverted index. The index is NOT automatically updated
-- when content_blocks changes; run `chatstrata reindex` after ingesting
-- new data to rebuild it.
--
-- FTS setup is intentionally deferred to `chatstrata reindex` so first-run
-- database creation never attempts an implicit DuckDB extension download.

SELECT 1;
