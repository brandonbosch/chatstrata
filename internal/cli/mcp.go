package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brandonbosch/chatstrata/internal/store"
)

// The MCP server mirrors chatstrata/mcp/server.py: a read-only `query` tool
// and the schema as the chatstrata://schema resource, plus a get_schema tool
// for clients that don't read resources.

const mcpInstructions = "You have access to a personal conversation archive stored in DuckDB. " +
	"Use the `query` tool to run read-only SQL against it. " +
	"Read the chatstrata://schema resource (or call `get_schema`) first to understand the tables, " +
	"column types, row counts, and relationships."

const queryToolDescription = `Run a read-only SQL query against the ChatStrata conversation archive.

The database contains conversations from Claude Code, claude.ai exports, Codex CLI,
OpenCode, Oh My Pi and Hermes Agent, normalized into a common schema.

Key tables:
- conversations: id, source_id, title, project, started_at, ended_at, message_count
- messages: id, conversation_id, role (user/assistant/system/tool), model, created_at
- content_blocks: id, message_id, type (text/tool_use/tool_result/thinking), text, tool_name, payload (JSON)
- tool_calls (VIEW): call_id, tool_name, input, conversation_id, project, created_at
- sources: id, name
- attachments: id, message_id, filename, mime_type
- divergences: conversations whose devices saw different continuations

Full-text search (BM25):
    WHERE fts_main_content_blocks.match_bm25(cb.id, 'search terms') IS NOT NULL
    ORDER BY fts_main_content_blocks.match_bm25(cb.id, 'search terms') DESC

Tips:
- Use LEFT(cb.text, 500) to truncate long text content
- tool_calls view is convenient for tool usage analysis
- payload column is JSON -- use ->> for extraction
- Only SELECT/WITH/DESCRIBE/SHOW/PRAGMA are allowed
- Results limited to 500 rows / 512 KB
- The BM25 index is rebuilt once enough content is new, so the newest
  blocks may be missing from it; ILIKE on cb.text always sees everything`

const exampleQueries = `-- Recent conversations
SELECT title, source_id, started_at, message_count
FROM conversations ORDER BY started_at DESC LIMIT 10;

-- Tool usage frequency
SELECT tool_name, COUNT(*) AS calls, COUNT(DISTINCT conversation_id) AS conversations
FROM tool_calls GROUP BY tool_name ORDER BY calls DESC;

-- Bash commands by project (Claude Code)
SELECT c.project, cb.payload->'input'->>'command' AS command, COUNT(*) AS times_run
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use' AND cb.tool_name = 'Bash' AND c.source_id = 'claude_code'
GROUP BY c.project, command ORDER BY times_run DESC LIMIT 30;

-- Full-text search (BM25) for a topic
SELECT c.title, c.started_at, cb.text
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE fts_main_content_blocks.match_bm25(cb.id, 'your search term') IS NOT NULL
ORDER BY fts_main_content_blocks.match_bm25(cb.id, 'your search term') DESC
LIMIT 10;

-- Messages per month by source
SELECT s.id AS source, date_trunc('month', m.created_at) AS month, COUNT(*) AS messages
FROM messages m
JOIN conversations c ON c.id = m.conversation_id
JOIN sources s ON s.id = c.source_id
WHERE m.created_at IS NOT NULL
GROUP BY source, month ORDER BY month DESC, source;

-- Most-used models
SELECT model, COUNT(*) AS messages FROM messages
WHERE model IS NOT NULL GROUP BY model ORDER BY messages DESC;

-- Conversation length distribution
SELECT
    CASE
        WHEN message_count < 5 THEN 'very short (< 5)'
        WHEN message_count < 20 THEN 'short (5-19)'
        WHEN message_count < 50 THEN 'medium (20-49)'
        ELSE 'long (50+)'
    END AS length_bucket,
    COUNT(*) AS conversations
FROM conversations GROUP BY length_bucket ORDER BY conversations DESC;

-- Per-project conversation counts
SELECT c.project, COUNT(*) AS conversations, SUM(c.message_count) AS total_messages
FROM conversations c WHERE c.project IS NOT NULL
GROUP BY c.project ORDER BY conversations DESC;
`

// archiveReader opens the archive read-only for one request. The daemon
// holds DuckDB's write lock for a few seconds per run, so a request that
// lands then waits for it rather than failing.
type archiveReader struct {
	dbPath string
	wait   time.Duration
}

func (a archiveReader) open(ctx context.Context) (*store.Store, error) {
	deadline := time.Now().Add(a.wait)
	for {
		s, err := store.OpenReadOnly(ctx, a.dbPath)
		if errors.Is(err, store.ErrNoArchive) {
			return nil, fmt.Errorf("ChatStrata database not found at %s. Run `chatstrata ingest <source>` first.", a.dbPath)
		}
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "lock") || time.Now().After(deadline) {
			if err == nil {
				s.LoadFTS(ctx)
			}
			return s, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type queryInput struct {
	SQL string `json:"sql" jsonschema:"the read-only SQL query to run"`
}

// queryResult is the JSON the query tool returns, field order as in Python.
type queryResult struct {
	Columns   []string          `json:"columns"`
	Rows      []json.RawMessage `json:"rows"`
	RowCount  int               `json:"row_count"`
	Truncated bool              `json:"truncated,omitempty"`
	Note      string            `json:"note,omitempty"`
}

func (a archiveReader) query(ctx context.Context, sqlText string) string {
	errorJSON := func(msg string) string {
		b, _ := marshalReadable(map[string]string{"error": msg})
		return string(b)
	}
	if err := validateReadOnly(sqlText); err != nil {
		return errorJSON(err.Error())
	}
	s, err := a.open(ctx)
	if err != nil {
		return errorJSON(err.Error())
	}
	defer s.Close()

	qctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := s.Conn().QueryContext(qctx, sqlText)
	if err != nil {
		if errors.Is(qctx.Err(), context.DeadlineExceeded) {
			return errorJSON(fmt.Sprintf("Query exceeded %s timeout.", queryTimeout))
		}
		return errorJSON("DuckDB error: " + err.Error())
	}
	defer rows.Close()
	cols, values, truncated, err := collect(rows)
	if err != nil {
		return errorJSON("DuckDB error: " + err.Error())
	}
	res := queryResult{Columns: cols, Rows: make([]json.RawMessage, 0, len(values)), RowCount: len(values)}
	if cols == nil {
		res.Columns = []string{}
	}
	for _, row := range values {
		obj, err := orderedRow(cols, row)
		if err != nil {
			return errorJSON(err.Error())
		}
		res.Rows = append(res.Rows, obj)
	}
	if truncated {
		res.Truncated = true
		res.Note = "Results were truncated. Add LIMIT or narrow your WHERE clause."
	}
	b, err := marshalReadable(res)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// marshalReadable is indented JSON without HTML escaping, so text such as
// "<source>" reaches the model as written, as Python's json.dumps leaves it.
func marshalReadable(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// orderedRow encodes a row as a JSON object with keys in column order.
func orderedRow(cols []string, row []any) (json.RawMessage, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := marshalCompact(c)
		v, err := marshalCompact(jsonValue(row[i]))
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String()), nil
}

func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// schema describes every table and view with its row count, as the Python
// chatstrata://schema resource does.
func (a archiveReader) schema(ctx context.Context) (string, error) {
	s, err := a.open(ctx)
	if err != nil {
		return "", err
	}
	defer s.Close()
	conn := s.Conn()
	rows, err := conn.QueryContext(ctx, `SELECT table_name, table_type FROM information_schema.tables
		WHERE table_schema = 'main' ORDER BY table_type, table_name`)
	if err != nil {
		return "", err
	}
	type table struct{ name, kind string }
	var tables []table
	for rows.Next() {
		var t table
		if err := rows.Scan(&t.name, &t.kind); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, t)
	}
	rows.Close()

	var schemaParts, statsParts []string
	for _, t := range tables {
		label := "TABLE"
		if t.kind == "VIEW" {
			label = "VIEW"
		}
		cols, err := conn.QueryContext(ctx, `SELECT column_name, data_type, is_nullable
			FROM information_schema.columns WHERE table_name = ? ORDER BY ordinal_position`, t.name)
		if err != nil {
			return "", err
		}
		var lines []string
		for cols.Next() {
			var name, dtype, nullable string
			if err := cols.Scan(&name, &dtype, &nullable); err != nil {
				cols.Close()
				return "", err
			}
			line := fmt.Sprintf("    %-30s %s", name, dtype)
			if nullable == "YES" {
				line += " (nullable)"
			}
			lines = append(lines, line)
		}
		cols.Close()
		schemaParts = append(schemaParts, fmt.Sprintf("%s %s:\n%s", label, t.name, strings.Join(lines, "\n")))

		var n int64
		if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t.name)).Scan(&n); err != nil {
			statsParts = append(statsParts, fmt.Sprintf("  %s: (unable to count)", t.name))
		} else {
			statsParts = append(statsParts, fmt.Sprintf("  %s: %s rows", t.name, thousands(n)))
		}
	}
	return fmt.Sprintf(`# ChatStrata Database Schema

%s

## Row Counts
%s

## Key Relationships
- conversations.source_id -> sources.id
- messages.conversation_id -> conversations.id
- content_blocks.message_id -> messages.id
- attachments.message_id -> messages.id
- tool_calls is a VIEW over content_blocks WHERE type = 'tool_use'

## Full-Text Search (BM25)
Use: fts_main_content_blocks.match_bm25(cb.id, 'your search terms')
Returns a float score (higher = more relevant), NULL for non-matches.
Filter with: WHERE fts_main_content_blocks.match_bm25(cb.id, 'term') IS NOT NULL
Order by score DESC for relevance ranking.

## Example Queries
%s
`, strings.Join(schemaParts, "\n\n"), strings.Join(statsParts, "\n"), exampleQueries), nil
}

// thousands formats n with comma separators, like Python's f"{n:,}".
func thousands(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// newMCPServer builds the server for an archive.
func newMCPServer(a archiveReader) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "ChatStrata", Version: Version},
		&mcp.ServerOptions{Instructions: mcpInstructions})
	mcp.AddTool(server, &mcp.Tool{Name: "query", Description: queryToolDescription},
		func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
			return text(a.query(ctx, in.SQL)), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "get_schema", Description: "Complete schema of the ChatStrata conversation archive: tables, column types, row counts, relationships and example queries."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			s, err := a.schema(ctx)
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
			}
			return text(s), nil, nil
		})
	server.AddResource(&mcp.Resource{
		URI: "chatstrata://schema", Name: "schema", MIMEType: "text/markdown",
		Description: "Complete schema of the ChatStrata conversation archive.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		s, err := a.schema(ctx)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: s}}}, nil
	})
	return server
}

func runServe(e *env, args []string) error {
	fs := newFlagSet(e, "serve", "serve [--transport stdio|streamable-http|sse] [--host HOST] [--port PORT] [--db PATH]")
	transport := fs.String("transport", "stdio", "MCP transport: stdio, streamable-http or sse.")
	host := fs.String("host", "127.0.0.1", "Host for HTTP transports.")
	port := fs.Int("port", 8462, "Port for HTTP transports.")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dbPath, err := store.ResolvePath(*db)
	if err != nil {
		return err
	}
	server := newMCPServer(archiveReader{dbPath: dbPath, wait: 15 * time.Second})

	switch *transport {
	case "stdio":
		return server.Run(e.ctx, &mcp.StdioTransport{})
	case "streamable-http", "sse":
	default:
		return usageError{"--transport must be stdio, streamable-http or sse"}
	}
	mux := http.NewServeMux()
	getServer := func(*http.Request) *mcp.Server { return server }
	path := "/mcp"
	if *transport == "sse" {
		path = "/sse"
		mux.Handle(path, mcp.NewSSEHandler(getServer, nil))
	} else {
		mux.Handle(path, mcp.NewStreamableHTTPHandler(getServer,
			&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	}
	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-e.ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(e.stderr, "ChatStrata MCP server on http://%s%s\n", addr, path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
