BEGIN TRANSACTION;
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_call_id TEXT,
    tool_calls TEXT,
    tool_name TEXT,
    timestamp REAL NOT NULL,
    finish_reason TEXT,
    reasoning TEXT,
    reasoning_content TEXT,
    active INTEGER NOT NULL DEFAULT 1,
    compacted INTEGER NOT NULL DEFAULT 0,
    _compressed_summary INTEGER NOT NULL DEFAULT 0,
    display_identity BLOB
);
INSERT INTO "messages" VALUES(1,'sess_1','system','You are a helpful agent.',NULL,NULL,NULL,1000.1,NULL,NULL,NULL,1,0,0,NULL);
INSERT INTO "messages" VALUES(2,'sess_1','user','The login endpoint returns 500. Please fix it.',NULL,NULL,NULL,1000.2,NULL,NULL,NULL,1,0,0,NULL);
INSERT INTO "messages" VALUES(3,'sess_1','assistant','Let me look at the auth code.',NULL,'[{"id": "call_1", "type": "function", "function": {"name": "search_files", "arguments": "{\"pattern\": \"auth\"}"}}]',NULL,1001.0,'tool_calls','The user reports a 500 on login.',NULL,1,0,0,NULL);
INSERT INTO "messages" VALUES(4,'sess_1','tool','/src/auth.py','call_1',NULL,'search_files',1001.5,NULL,NULL,NULL,1,0,0,NULL);
INSERT INTO "messages" VALUES(5,'sess_1','assistant','outdated draft reply',NULL,NULL,NULL,1002.0,'stop',NULL,NULL,0,0,0,NULL);
INSERT INTO "messages" VALUES(6,'sess_1','assistant','Summary of earlier turns: the login bug was diagnosed.',NULL,NULL,NULL,1002.5,'stop',NULL,NULL,1,0,1,NULL);
INSERT INTO "messages" VALUES(7,'sess_1','assistant','Fixed the bug in auth.py.',NULL,NULL,NULL,1003.0,'stop','The fix is a one-liner.',NULL,1,0,0,NULL);
INSERT INTO "messages" VALUES(8,'sess_1','user','',NULL,NULL,NULL,1004.0,NULL,NULL,NULL,1,0,0,NULL);
INSERT INTO "messages" VALUES(9,'sess_1','user','earlier context (compacted)',NULL,NULL,NULL,999.0,NULL,NULL,NULL,1,1,0,NULL);
INSERT INTO "messages" VALUES(10,'sess_2','user','hello',NULL,NULL,NULL,2000.5,NULL,NULL,NULL,1,0,0,NULL);
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    display_name TEXT,
    model TEXT,
    system_prompt TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    message_count INTEGER DEFAULT 0,
    tool_call_count INTEGER DEFAULT 0,
    cwd TEXT,
    profile_name TEXT,
    title TEXT
);
INSERT INTO "sessions" VALUES('sess_1','cli',NULL,'test-model',NULL,1000.0,1005.0,6,2,'/home/example/project','default','Fix the login bug');
INSERT INTO "sessions" VALUES('sess_2','telegram',NULL,'test-model',NULL,2000.0,NULL,1,0,NULL,'default','Plan sprint');
DELETE FROM "sqlite_sequence";
INSERT INTO "sqlite_sequence" VALUES('messages',10);
COMMIT;
