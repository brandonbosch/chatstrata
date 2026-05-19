-- chatstrata schema v2: full-text search index on content_blocks.text
--
-- The FTS extension creates internal tables (fts_main_content_blocks_*)
-- that store the inverted index. The index is NOT automatically updated
-- when content_blocks changes; run `chatstrata reindex` after ingesting
-- new data to rebuild it.

LOAD fts;

PRAGMA create_fts_index(
    'content_blocks', 'id', 'text',
    stemmer = 'porter',
    stopwords = 'english',
    overwrite = 1
);
