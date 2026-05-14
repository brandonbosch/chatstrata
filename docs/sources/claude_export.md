# Claude Export (claude.ai)

## What it ingests

Conversations from the official Anthropic data export. This covers all
conversations you've had through the claude.ai web and mobile interfaces.

## How to export

1. Go to [claude.ai](https://claude.ai)
2. Click your profile icon → Settings → Account
3. Click "Export data"
4. Anthropic sends an email with a download link (may take a few minutes)
5. Download and unzip the archive

The unzipped archive contains:
- `conversations.json` — all your conversations (this is what chatstrata ingests)
- `users.json` — your account info
- `projects.json` — project metadata

## Ingesting

Point chatstrata at the unzipped export directory:

    chatstrata ingest claude_export --path ~/Downloads/data-export

Or directly at the conversations file:

    chatstrata ingest claude_export --path ~/Downloads/data-export/conversations.json

## What gets extracted

- Conversation title (or fallback to first message)
- All human and assistant messages with timestamps
- Structured content blocks (text, tool use, tool results)
- File attachments (metadata and extracted text content)
- Full raw conversation objects preserved in raw_events for future re-parsing

## Limitations

- **No model information**: The export does not include which Claude model
  was used for each response. The `model` field will be null.
- **No project association**: Unlike Claude Code, claude.ai conversations
  don't have a project/cwd concept. The `project` field will be null.
- **Attachments are metadata-only**: The export includes attachment
  filenames, sizes, and extracted text but not the original file bytes.
- **Point-in-time snapshot**: The export captures conversations as of the
  export time. Re-export to get newer conversations.

## Idempotency

Re-running `chatstrata ingest claude_export` with the same export file
is safe. Conversations are matched by their UUID — existing data is
updated, not duplicated.
