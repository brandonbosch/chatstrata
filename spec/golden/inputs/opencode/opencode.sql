BEGIN TRANSACTION;
CREATE TABLE message (
            id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            data TEXT NOT NULL
        );
INSERT INTO "message" VALUES('msg_user001','ses_test001',1700000000000,1700000000000,'{"role": "user", "time": {"created": 1700000000000}, "summary": {"diffs": []}, "agent": "build", "model": {"providerID": "llamacpp", "modelID": "qwen-distill"}}');
INSERT INTO "message" VALUES('msg_asst001','ses_test001',1700000010000,1700000060000,'{"role": "assistant", "time": {"created": 1700000010000, "completed": 1700000060000}, "parentID": "msg_user001", "modelID": "qwen-distill", "providerID": "llamacpp", "mode": "build", "agent": "build", "path": {"cwd": "/home/user/projects/myapp", "root": "/"}, "cost": 0, "tokens": {"total": 5000, "input": 4500, "output": 500, "reasoning": 0, "cache": {"read": 0, "write": 0}}, "finish": "tool-calls"}');
CREATE TABLE part (
            id TEXT PRIMARY KEY,
            message_id TEXT NOT NULL,
            session_id TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            data TEXT NOT NULL
        );
INSERT INTO "part" VALUES('prt_user001','msg_user001','ses_test001',1700000000000,1700000000000,'{"type": "text", "text": "Add error handling to the database connection module"}');
INSERT INTO "part" VALUES('prt_step001','msg_asst001','ses_test001',1700000010000,1700000010000,'{"type": "step-start"}');
INSERT INTO "part" VALUES('prt_reason001','msg_asst001','ses_test001',1700000011000,1700000015000,'{"type": "reasoning", "text": "I need to add try-except blocks around the database connection calls.", "time": {"start": 1700000011000, "end": 1700000015000}}');
INSERT INTO "part" VALUES('prt_text001','msg_asst001','ses_test001',1700000015000,1700000015000,'{"type": "text", "text": "I''ll add error handling to the database connection module.", "time": {"start": 1700000015000, "end": 1700000015000}}');
INSERT INTO "part" VALUES('prt_tool001','msg_asst001','ses_test001',1700000020000,1700000025000,'{"type": "tool", "callID": "call_abc123", "tool": "bash", "state": {"status": "completed", "input": {"command": "cat db.py", "description": "Read database module"}, "output": "import sqlite3\n\ndef connect():\n    return sqlite3.connect(\"app.db\")", "title": "Read database module", "metadata": {"output": "import sqlite3\n\ndef connect():\n    return sqlite3.connect(\"app.db\")", "exit": 0, "description": "Read database module", "truncated": false}, "time": {"start": 1700000020000, "end": 1700000025000}}}');
INSERT INTO "part" VALUES('prt_text002','msg_asst001','ses_test001',1700000030000,1700000030000,'{"type": "text", "text": "I''ve reviewed the module. Adding try-except blocks now.", "time": {"start": 1700000030000, "end": 1700000030000}}');
INSERT INTO "part" VALUES('prt_stepfin001','msg_asst001','ses_test001',1700000035000,1700000035000,'{"type": "step-finish", "reason": "tool-calls", "snapshot": "abc123", "cost": 0, "tokens": {"total": 5000, "input": 4500, "output": 500, "reasoning": 0, "cache": {"read": 0, "write": 0}}}');
INSERT INTO "part" VALUES('prt_patch001','msg_asst001','ses_test001',1700000040000,1700000040000,'{"type": "patch", "hash": "deadbeef1234567890", "files": ["/home/user/projects/myapp/db.py"]}');
CREATE TABLE project (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL
        );
INSERT INTO "project" VALUES('proj_001','myapp');
CREATE TABLE session (
            id TEXT PRIMARY KEY,
            project_id TEXT NOT NULL,
            parent_id TEXT,
            slug TEXT NOT NULL,
            directory TEXT NOT NULL,
            title TEXT NOT NULL,
            version TEXT NOT NULL,
            share_url TEXT,
            summary_additions INTEGER,
            summary_deletions INTEGER,
            summary_files INTEGER,
            summary_diffs TEXT,
            revert TEXT,
            permission TEXT,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            time_compacting INTEGER,
            time_archived INTEGER,
            workspace_id TEXT
        );
INSERT INTO "session" VALUES('ses_test001','proj_001',NULL,'add-error-handling','/home/user/projects/myapp','Add error handling to database module','1.0.0',NULL,NULL,NULL,NULL,NULL,NULL,NULL,1700000000000,1700001000000,NULL,NULL,NULL);
COMMIT;
