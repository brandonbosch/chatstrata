BEGIN TRANSACTION;
-- Legacy (OpenCode 1.x) tables and session, as in inputs/opencode.
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
-- OpenCode 2.x backfills legacy sessions into session_v2 under the same id;
-- this legacy copy of the v2 session must be ignored in favour of session_v2.
INSERT INTO "session" VALUES('ses_eed07b19affeqL6wrtBlxM40fP','proj_001',NULL,'legacy-copy','/tmp/chatstrata-oc2-throwaway','legacy copy (must not appear)','1.0.0',NULL,NULL,NULL,NULL,NULL,NULL,NULL,1791319625346,1791319625382,NULL,NULL,NULL);
INSERT INTO "message" VALUES('msg_legacy_copy','ses_eed07b19affeqL6wrtBlxM40fP',1791319625396,1791319625396,'{"role": "user", "time": {"created": 1791319625396}}');
INSERT INTO "part" VALUES('prt_legacy_copy','msg_legacy_copy','ses_eed07b19affeqL6wrtBlxM40fP',1791319625396,1791319625396,'{"type": "text", "text": "LEGACY COPY: must not appear"}');
-- OpenCode 2.x tables (schema from captures/opencode_v2/oc-schema.sql) and the
-- captured throwaway session.
CREATE TABLE `session_v2` (
          `id` text PRIMARY KEY,
          `project_id` text NOT NULL,
          `workspace_id` text,
          `parent_id` text,
          `fork_session_id` text,
          `fork_boundary` text,
          `slug` text NOT NULL,
          `directory` text NOT NULL,
          `path` text,
          `title` text,
          `version` text NOT NULL,
          `share_url` text,
          `summary_additions` integer,
          `summary_deletions` integer,
          `summary_files` integer,
          `summary_diffs` text,
          `metadata` text,
          `cost` real DEFAULT 0 NOT NULL,
          `tokens_input` integer DEFAULT 0 NOT NULL,
          `tokens_output` integer DEFAULT 0 NOT NULL,
          `tokens_reasoning` integer DEFAULT 0 NOT NULL,
          `tokens_cache_read` integer DEFAULT 0 NOT NULL,
          `tokens_cache_write` integer DEFAULT 0 NOT NULL,
          `revert` text,
          `permission` text,
          `agent` text,
          `model` text,
          `time_created` integer NOT NULL,
          `time_updated` integer NOT NULL,
          `time_compacting` integer,
          `time_archived` integer,
          `time_suspended` integer, `resume_attempts` integer DEFAULT 0 NOT NULL, `time_idle` integer, `time_viewed` integer, `idle_outcome` text,
          CONSTRAINT `fk_session_v2_project_id_project_id_fk` FOREIGN KEY (`project_id`) REFERENCES `project`(`id`) ON DELETE CASCADE
        );
CREATE TABLE "session_message" (
          `id` text PRIMARY KEY,
          `session_id` text NOT NULL,
          `type` text NOT NULL,
          `seq` integer NOT NULL,
          `time_created` integer NOT NULL,
          `time_updated` integer NOT NULL,
          `data` text NOT NULL,
          CONSTRAINT `fk_session_message_session_id_session_v2_id_fk` FOREIGN KEY (`session_id`) REFERENCES `session_v2`(`id`) ON DELETE CASCADE
        );
CREATE UNIQUE INDEX `session_message_session_seq_idx` ON `session_message` (`session_id`,`seq`);
CREATE INDEX `session_message_session_type_seq_idx` ON `session_message` (`session_id`,`type`,`seq`);
CREATE INDEX `session_message_session_time_created_id_idx` ON `session_message` (`session_id`,`time_created`,`id`);
CREATE INDEX `session_message_time_created_idx` ON `session_message` (`time_created`);
CREATE INDEX `session_v2_project_idx` ON `session_v2` (`project_id`);
CREATE INDEX `session_v2_workspace_idx` ON `session_v2` (`workspace_id`);
CREATE INDEX `session_v2_parent_idx` ON `session_v2` (`parent_id`);
CREATE INDEX `session_v2_time_suspended_idx` ON `session_v2` (`time_suspended`) WHERE "session_v2"."time_suspended" is not null;
INSERT INTO "session_v2" VALUES('ses_eed07b19affeqL6wrtBlxM40fP','62be6de141bedc44a6210f1ad43b67ba5e1fcd73',NULL,NULL,NULL,NULL,'stellar-lagoon','/tmp/chatstrata-oc2-throwaway','','chatstrata oc2 fixture','2.0.12',NULL,NULL,NULL,NULL,NULL,NULL,0.00213123,9941,80,52,19136,0,NULL,NULL,NULL,'{"id":"z-ai/glm-5.3-flash","providerID":"openrouter"}',1791319625346,1791319625382,NULL,NULL,NULL,0,1791319649882,NULL,'succeeded');
INSERT INTO "session_message" VALUES('msg_112f84e89001Xoi4Q9FR7Zqk4x','ses_eed07b19affeqL6wrtBlxM40fP','user',5,1791319625396,1791319625396,'{"time":{"created":1791319625396},"text":"\"Create a file hello.txt containing hi, then read it back and tell me what it says\"","files":[]}');
INSERT INTO "session_message" VALUES('msg_112f84eb9001VX3X0anTdlXmEf','ses_eed07b19affeqL6wrtBlxM40fP','assistant',6,1791319625407,1791319626927,'{"time":{"created":1791319625407,"streamed":1791319626922,"completed":1791319626927},"agent":"build","model":{"id":"z-ai/glm-5.3-flash","providerID":"openrouter","variant":"default"},"content":[{"type":"reasoning","text":"The user wants me to create hello.txt with \"hi\", then read it back. Working directory is /tmp/chatstrata-oc2-throwaway. I''ll use the write tool, then read it.","state":{"reasoningField":"reasoning","reasoningDetails":[{"type":"reasoning.text","text":"The user wants me to create hello.txt with \"hi\", then read it back. Working directory is /tmp/chatstrata-oc2-throwaway. I''ll use the write tool, then read it.","format":"unknown","index":0}]},"time":{"created":1791319626206,"completed":1791319626918}},{"type":"tool","id":"call_01a112f853b77521a1b88575","name":"write","executed":false,"state":{"status":"completed","input":{"path":"/tmp/chatstrata-oc2-throwaway/hello.txt","content":"hi"},"content":[{"type":"text","text":"Created file successfully: hello.txt"}],"metadata":{"truncated":false}},"time":{"created":1791319626905,"ran":1791319626920,"completed":1791319626925}}],"finish":"tool-calls","rawFinish":"tool_calls","cost":0.001479,"tokens":{"input":9620,"output":29,"reasoning":43,"cache":{"read":0,"write":0}}}');
INSERT INTO "session_message" VALUES('msg_112f854b5001mjmnEowEQE9WvY','ses_eed07b19affeqL6wrtBlxM40fP','assistant',15,1791319626938,1791319628688,'{"time":{"created":1791319626938,"streamed":1791319628683,"completed":1791319628687},"agent":"build","model":{"id":"z-ai/glm-5.3-flash","providerID":"openrouter","variant":"default"},"content":[{"type":"tool","id":"call_01a112f85b4e74bba54d1f51","name":"read","executed":false,"state":{"status":"completed","input":{"path":"/tmp/chatstrata-oc2-throwaway/hello.txt"},"content":[{"type":"text","text":"Read file /tmp/chatstrata-oc2-throwaway/hello.txt, lines 1-1\n1: hi"}],"metadata":{"truncated":false}},"time":{"created":1791319628678,"ran":1791319628682,"completed":1791319628686}}],"finish":"tool-calls","rawFinish":"tool_calls","cost":0.00032247999999999993,"tokens":{"input":166,"output":23,"reasoning":0,"cache":{"read":9536,"write":0}}}');
INSERT INTO "session_message" VALUES('msg_112f85b95001UED2i7ezsgS03a','ses_eed07b19affeqL6wrtBlxM40fP','assistant',22,1791319628697,1791319649880,'{"time":{"created":1791319628697,"streamed":1791319649879,"completed":1791319649880},"agent":"build","model":{"id":"z-ai/glm-5.3-flash","providerID":"openrouter","variant":"default"},"content":[{"type":"reasoning","text":"The file says \"hi\". Simple confirmation.","state":{"reasoningField":"reasoning","reasoningDetails":[{"type":"reasoning.text","text":"The file says \"hi\". Simple confirmation.","format":"unknown","index":0}]},"time":{"created":1791319647929,"completed":1791319649876}},{"type":"text","text":"Done. I created `hello.txt` containing `hi`, read it back, and it says exactly that: **hi**."}],"finish":"stop","rawFinish":"stop","cost":0.00032975,"tokens":{"input":155,"output":28,"reasoning":9,"cache":{"read":9600,"write":0}}}');
INSERT INTO "session_message" VALUES('msg_112f8ae5a001FdWqF58Lk0uqWg','ses_eed07b19affeqL6wrtBlxM40fP','idle',29,1791319649882,1791319649882,'{"time":{"created":1791319649882},"outcome":"succeeded"}');
COMMIT;
