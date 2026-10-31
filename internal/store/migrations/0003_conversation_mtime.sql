-- chatstrata schema v3: track source file modification time for incremental ingest
ALTER TABLE conversations ADD COLUMN source_file_mtime DOUBLE;
