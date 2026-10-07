// Package hermes reads Hermes Agent's SQLite session stores.
//
// Hermes keeps sessions in $HERMES_HOME/state.db (default ~/.hermes) and in
// profiles/<name>/state.db for each named profile. A session's rows are
// serialized as one JSON document, which is what the collector logs and
// Parse reads. This is a port of chatstrata/sources/hermes_agent/adapter.py
// and must produce the same archive (see spec/golden).
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// DefaultHome is the Hermes home when $HERMES_HOME is unset, relative to $HOME.
const DefaultHome = ".hermes"

// profileID mirrors Hermes's own rule for named profile directories.
var profileID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Source struct{}

func (Source) Name() string        { return "hermes_agent" }
func (Source) DisplayName() string { return "Hermes Agent" }
func (Source) Version() string     { return "0.1.0" }

// AppendOnly is false: a session is logged whole when it changes.
func (Source) AppendOnly() bool { return false }

type store struct{ profile, path string }

// root is the Hermes home that owns the profile tree. A $HERMES_HOME that
// points at a named profile resolves to its root, so the default store and
// sibling profiles stay visible.
func root() (string, error) {
	env := strings.TrimSpace(os.Getenv("HERMES_HOME"))
	if env == "" {
		return srcutil.Root("", DefaultHome)
	}
	home, err := srcutil.ExpandHome(env)
	if err != nil {
		return "", err
	}
	if filepath.Base(filepath.Dir(home)) == "profiles" && profileID.MatchString(filepath.Base(home)) {
		return filepath.Dir(filepath.Dir(home)), nil
	}
	return home, nil
}

// candidates are the stores to read, in order: an explicit path alone, or the
// default store and every named profile.
func candidates(path string) ([]store, error) {
	if path != "" {
		p, err := srcutil.ExpandHome(path)
		return []store{{"default", p}}, err
	}
	r, err := root()
	if err != nil {
		return nil, err
	}
	out := []store{{"default", filepath.Join(r, "state.db")}}
	entries, _ := os.ReadDir(filepath.Join(r, "profiles"))
	for _, e := range entries { // ReadDir sorts by name
		if e.IsDir() && e.Name() != "default" && profileID.MatchString(e.Name()) {
			out = append(out, store{e.Name(), filepath.Join(r, "profiles", e.Name(), "state.db")})
		}
	}
	return out, nil
}

// nativeID qualifies session ids from named profiles, since a cloned profile
// copies session ids verbatim.
func nativeID(profile, id string) string {
	if profile == "default" {
		return id
	}
	return profile + "/" + id
}

// document is what the log holds for one session: its rows, as read.
type document struct {
	Session  srcutil.Row   `json:"session"`
	Messages []srcutil.Row `json:"messages"`
}

// Discover lists sessions across every store. Finding no store at all is
// ErrNotFound; a store that can't be read, or isn't a Hermes store, is an
// error rather than an empty result.
func (Source) Discover(path string) ([]model.Handle, error) {
	stores, err := candidates(path)
	if err != nil {
		return nil, err
	}
	var present []store
	var searched []string
	for _, s := range stores {
		searched = append(searched, s.path)
		if _, err := os.Stat(s.path); err == nil {
			present = append(present, s)
		}
	}
	if len(present) == 0 {
		return nil, fmt.Errorf("Hermes state database %w: %s (set HERMES_HOME, or pass --path to pick one store)",
			model.ErrNotFound, strings.Join(searched, ", "))
	}
	var handles []model.Handle
	for _, s := range present {
		hs, err := discoverStore(s)
		if err != nil {
			return nil, err
		}
		handles = append(handles, hs...)
	}
	return handles, nil
}

func discoverStore(s store) ([]model.Handle, error) {
	db, err := srcutil.OpenSQLite(s.path)
	if err != nil {
		return nil, fmt.Errorf("Hermes state database is not readable: %s (%w)", s.path, err)
	}
	defer db.Close()
	ctx := context.Background()
	ids, err := srcutil.QueryRows(ctx, db, "SELECT id FROM sessions ORDER BY started_at")
	if err != nil {
		return nil, fmt.Errorf("Hermes state database has no usable 'sessions' table: %s (%w)", s.path, err)
	}
	handles := make([]model.Handle, 0, len(ids))
	for _, row := range ids {
		id := fmt.Sprint(row.Get("id"))
		var doc document
		sessions, err := srcutil.QueryRows(ctx, db, "SELECT * FROM sessions WHERE id = ?", id)
		if err != nil || len(sessions) == 0 {
			return nil, fmt.Errorf("read session %s from %s: %v", id, s.path, err)
		}
		doc.Session = sessions[0]
		if doc.Messages, err = srcutil.QueryRows(ctx, db,
			"SELECT * FROM messages WHERE session_id = ? ORDER BY timestamp, id", id); err != nil {
			return nil, fmt.Errorf("read messages of %s from %s: %w", id, s.path, err)
		}
		content, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		handles = append(handles, model.Handle{SourceNativeID: nativeID(s.profile, id), Path: s.path, Content: content})
	}
	return handles, nil
}

var roles = map[string]model.Role{
	"user":      model.RoleUser,
	"assistant": model.RoleAssistant,
	"system":    model.RoleSystem,
	"tool":      model.RoleTool,
}

// Parse builds a conversation from a session's rows.
func (Source) Parse(h model.Handle, content []byte) (*model.Conversation, error) {
	var doc struct {
		Session  json.RawMessage   `json:"session"`
		Messages []json.RawMessage `json:"messages"`
	}
	if obj, ok := srcutil.DecodeObject(bytes.TrimSpace(content)); ok && obj["session"] != nil {
		if err := json.Unmarshal(content, &doc); err != nil {
			return nil, fmt.Errorf("read session document: %w", err)
		}
	} else {
		// A legacy import: the Python archive's raw_events, the session row
		// and then each message row, one per line.
		for i, ev := range srcutil.ReadJSONL(content) {
			if i == 0 {
				doc.Session = ev.Raw
			} else {
				doc.Messages = append(doc.Messages, ev.Raw)
			}
		}
	}
	session, ok := srcutil.DecodeObject(doc.Session)
	if !ok {
		return nil, fmt.Errorf("read session document: no session row")
	}

	conv := &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		Project:        asString(session["cwd"]),
		RawPath:        h.Path,
		Metadata: map[string]any{
			"hermes_source":         session["source"],
			"model":                 session["model"],
			"session_message_count": session["message_count"],
			"tool_call_count":       session["tool_call_count"],
		},
		RawEvents: []json.RawMessage{doc.Session},
	}
	for _, raw := range doc.Messages {
		conv.RawEvents = append(conv.RawEvents, raw)
		row, ok := srcutil.DecodeObject(raw)
		if !ok {
			continue
		}
		// Inactive rows that were never compacted are superseded versions
		// (after a rewind, say), no longer part of the conversation.
		if !srcutil.Truthy(row["active"]) && !srcutil.Truthy(row["compacted"]) {
			continue
		}
		role, blocks := messageBlocks(row)
		if role == "" || len(blocks) == 0 {
			continue
		}
		metadata := map[string]any{
			"hermes_message_id": row["id"],
			"finish_reason":     row["finish_reason"],
			"source":            session["source"],
			"profile_name":      session["profile_name"],
		}
		if srcutil.Truthy(row["compacted"]) {
			metadata["compacted"] = true
		}
		if srcutil.Truthy(row["_compressed_summary"]) {
			metadata["compressed_summary"] = true
		}
		msg := model.Message{
			SourceNativeID: model.Ptr(fmt.Sprint(row["id"])),
			Role:           role,
			CreatedAt:      seconds(row["timestamp"]),
			Blocks:         blocks,
			Metadata:       metadata,
		}
		if role == model.RoleAssistant {
			msg.Model = asString(session["model"])
		}
		conv.Messages = append(conv.Messages, msg)
	}

	conv.StartedAt, conv.EndedAt = seconds(session["started_at"]), seconds(session["ended_at"])
	if conv.StartedAt == nil || conv.EndedAt == nil {
		for _, m := range conv.Messages {
			t := m.CreatedAt
			if t == nil {
				continue
			}
			if conv.StartedAt == nil || t.Before(*conv.StartedAt) {
				conv.StartedAt = t
			}
			if conv.EndedAt == nil || t.After(*conv.EndedAt) {
				conv.EndedAt = t
			}
		}
	}

	title := session["title"]
	if !srcutil.Truthy(title) {
		title = session["display_name"]
	}
	if srcutil.Truthy(title) {
		conv.Title = asString(title)
	} else if line := srcutil.FirstUserLine(conv.Messages); line != nil {
		conv.Title = line
	} else {
		conv.Title = asString(title)
	}
	return conv, nil
}

func asString(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// seconds converts a Unix-seconds column; null or unparsable values are nil.
func seconds(v any) *time.Time {
	var f float64
	switch x := v.(type) {
	case json.Number:
		var err error
		if f, err = x.Float64(); err != nil {
			return nil
		}
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return nil
		}
	default:
		return nil
	}
	t := srcutil.UnixTime(f)
	return &t
}

// toolCalls parses the tool_calls column, tolerating malformed values.
func toolCalls(v any) []map[string]any {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var list []any
	if dec.Decode(&list) != nil {
		return nil
	}
	var calls []map[string]any
	for _, c := range list {
		if m, ok := c.(map[string]any); ok {
			calls = append(calls, m)
		}
	}
	return calls
}

func messageBlocks(row map[string]any) (model.Role, []model.Block) {
	name, _ := row["role"].(string)
	role, ok := roles[name]
	if !ok {
		return "", nil
	}
	var blocks []model.Block
	if role == model.RoleAssistant {
		for _, call := range toolCalls(row["tool_calls"]) {
			fn, ok := srcutil.GetOr(call, "function", nil).(map[string]any)
			if !ok {
				if srcutil.Truthy(call["function"]) {
					continue
				}
				fn = map[string]any{}
			}
			id := call["id"]
			if !srcutil.Truthy(id) {
				id = call["call_id"]
			}
			blocks = append(blocks, model.Block{
				Type:      model.BlockToolUse,
				ToolName:  asString(fn["name"]),
				ToolUseID: asString(id),
				Payload:   map[string]any{"arguments": srcutil.GetOr(fn, "arguments", "")},
			})
		}
	}
	text := row["content"]
	hasText := srcutil.Truthy(text)
	if role == model.RoleTool {
		var t *string
		if hasText {
			t = asString(text)
		}
		// The output goes into the tool result rather than a text block.
		return role, append(blocks, model.Block{
			Type:      model.BlockToolResult,
			ToolUseID: asString(row["tool_call_id"]),
			ToolName:  asString(row["tool_name"]),
			Text:      t,
		})
	}
	if hasText {
		blocks = append(blocks, model.Block{Type: model.BlockText, Text: asString(text)})
	}
	if role == model.RoleAssistant {
		reasoning := row["reasoning"]
		if !srcutil.Truthy(reasoning) {
			reasoning = row["reasoning_content"]
		}
		if srcutil.Truthy(reasoning) {
			blocks = append(blocks, model.Block{Type: model.BlockThinking, Text: asString(reasoning)})
		}
	}
	return role, blocks
}
