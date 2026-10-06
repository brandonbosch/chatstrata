// Package opencode reads OpenCode sessions from its SQLite database
// (~/.local/share/opencode/opencode.db).
//
// Sessions live in the session table, turns in message and content units in
// part, with JSON in their data columns and times in Unix milliseconds. A
// session's rows are serialized as one JSON document, which is what the
// collector logs and Parse reads. This is a port of
// chatstrata/sources/opencode/adapter.py and must produce the same archive
// (see spec/golden).
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// DefaultPath is OpenCode's database, relative to $HOME.
const DefaultPath = ".local/share/opencode/opencode.db"

type Source struct{}

func (Source) Name() string        { return "opencode" }
func (Source) DisplayName() string { return "OpenCode" }
func (Source) Version() string     { return "0.1.0" }

// AppendOnly is false: a session is logged whole when it changes.
func (Source) AppendOnly() bool { return false }

// document is what the log holds for one session: its rows, as read.
type document struct {
	Session  srcutil.Row   `json:"session"`
	Messages []srcutil.Row `json:"messages"`
	Parts    []srcutil.Row `json:"parts"`
}

// Discover reads every session from the database.
func (Source) Discover(path string) ([]model.Handle, error) {
	path, err := srcutil.Root(path, DefaultPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := srcutil.OpenSQLite(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()
	ctx := context.Background()
	sessions, err := srcutil.QueryRows(ctx, db,
		"SELECT id, title, directory, time_created, time_updated FROM session ORDER BY time_created")
	if err != nil {
		return nil, fmt.Errorf("read sessions from %s: %w", path, err)
	}
	handles := make([]model.Handle, 0, len(sessions))
	for _, s := range sessions {
		id := fmt.Sprint(s.Get("id"))
		doc := document{Session: s}
		if doc.Messages, err = srcutil.QueryRows(ctx, db,
			"SELECT id, data, time_created FROM message WHERE session_id = ? ORDER BY time_created", id); err != nil {
			return nil, fmt.Errorf("read messages of %s: %w", id, err)
		}
		if doc.Parts, err = srcutil.QueryRows(ctx, db,
			"SELECT id, message_id, data, time_created FROM part WHERE session_id = ? ORDER BY time_created", id); err != nil {
			return nil, fmt.Errorf("read parts of %s: %w", id, err)
		}
		content, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		handles = append(handles, model.Handle{SourceNativeID: id, Path: path, Content: content})
	}
	return handles, nil
}

type rawRow map[string]any

// decodedDoc is document read back.
type decodedDoc struct {
	Session  rawRow   `json:"session"`
	Messages []rawRow `json:"messages"`
	Parts    []rawRow `json:"parts"`
}

// decode reads a session document, or a legacy import: the Python
// archive's raw_events, one message per line with its parts nested and its
// data already decoded. Legacy imports carry no session row, so the title is
// missing and the project comes from the import's project hint.
func decode(content []byte, h model.Handle) (decodedDoc, error) {
	var doc decodedDoc
	if obj, ok := srcutil.DecodeObject(bytes.TrimSpace(content)); ok {
		if _, isDoc := obj["session"]; isDoc {
			dec := json.NewDecoder(bytes.NewReader(content))
			dec.UseNumber()
			if err := dec.Decode(&doc); err != nil {
				return doc, fmt.Errorf("read session document: %w", err)
			}
			return doc, nil
		}
	}
	doc.Session = rawRow{}
	if h.Project != "" {
		doc.Session["directory"] = h.Project
	}
	for _, ev := range srcutil.ReadJSONL(content) {
		msg := ev.Fields
		doc.Messages = append(doc.Messages, rawRow{"id": msg["id"], "data": msg["data"], "time_created": msg["time_created"]})
		parts, _ := msg["parts"].([]any)
		for _, p := range parts {
			if part, ok := p.(map[string]any); ok {
				doc.Parts = append(doc.Parts, rawRow{"id": part["id"], "message_id": msg["id"], "data": part["data"]})
			}
		}
	}
	return doc, nil
}

// parseData decodes a JSON data column (already decoded in legacy imports);
// anything else reads as {}.
func parseData(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	s, ok := v.(string)
	if !ok {
		return map[string]any{}
	}
	m, ok := srcutil.DecodeObject([]byte(s))
	if !ok {
		return map[string]any{}
	}
	return m
}

func millis(v any) (int64, bool) {
	if !srcutil.Truthy(v) {
		return 0, false
	}
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		f, err := x.Float64()
		return int64(f), err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func orAny(a, b any) any {
	if srcutil.Truthy(a) {
		return a
	}
	return b
}

func asString(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// Parse builds a conversation from a session's rows.
func (Source) Parse(h model.Handle, content []byte) (*model.Conversation, error) {
	doc, err := decode(content, h)
	if err != nil {
		return nil, err
	}

	type part struct {
		id   any
		data map[string]any
	}
	parts := map[string][]part{}
	for _, p := range doc.Parts {
		mid := fmt.Sprint(p["message_id"])
		parts[mid] = append(parts[mid], part{id: p["id"], data: parseData(p["data"])})
	}

	conv := &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		Title:          asString(doc.Session["title"]),
		Project:        asString(doc.Session["directory"]),
		RawPath:        h.Path,
		Metadata:       map[string]any{},
	}
	var span srcutil.Span
	for _, row := range doc.Messages {
		data := parseData(row["data"])
		msgID := fmt.Sprint(row["id"])
		msgParts := parts[msgID]

		rawParts := make([]map[string]any, 0, len(msgParts))
		for _, p := range msgParts {
			rawParts = append(rawParts, map[string]any{"id": p.id, "data": p.data})
		}
		raw, err := json.Marshal(map[string]any{
			"id": row["id"], "data": data, "time_created": row["time_created"], "parts": rawParts,
		})
		if err != nil {
			return nil, err
		}
		conv.RawEvents = append(conv.RawEvents, raw)

		timeInfo := srcutil.Map(data, "time")
		ts := unixMillis(orAny(timeInfo["created"], row["time_created"]))
		span.Add(ts)
		if done := unixMillis(timeInfo["completed"]); done != nil {
			if span.End == nil || done.After(*span.End) {
				span.End = done
			}
		}

		var modelID, providerID any
		if info, ok := data["model"].(map[string]any); ok {
			modelID = orAny(info["modelID"], data["modelID"])
			providerID = orAny(info["providerID"], data["providerID"])
		} else {
			modelID, providerID = data["modelID"], data["providerID"]
		}

		role := model.RoleAssistant
		if data["role"] == "user" {
			role = model.RoleUser
		}
		var msgModel *string
		if role == model.RoleAssistant {
			msgModel = asString(modelID)
		}
		metadata := map[string]any{}
		if srcutil.Truthy(providerID) {
			metadata["provider_id"] = providerID
		}
		for _, k := range []string{"agent", "mode", "tokens"} {
			if srcutil.Truthy(data[k]) {
				metadata[k] = data[k]
			}
		}

		var pending []model.Block
		flush := func() {
			if len(pending) == 0 {
				return
			}
			conv.Messages = append(conv.Messages, model.Message{
				SourceNativeID: model.Ptr(msgID),
				Role:           role,
				Model:          msgModel,
				CreatedAt:      ts,
				Blocks:         pending,
				Metadata:       metadata,
			})
			pending = nil
		}
		for _, p := range msgParts {
			switch p.data["type"] {
			case "step-start", "step-finish":
			case "tool":
				flush()
				conv.Messages = append(conv.Messages, toolMessages(p.data, msgModel, ts)...)
			default:
				pending = append(pending, partBlocks(p.data)...)
			}
		}
		flush()
	}
	conv.StartedAt, conv.EndedAt = span.Start, span.End
	return conv, nil
}

func unixMillis(v any) *time.Time {
	ms, ok := millis(v)
	if !ok {
		return nil
	}
	t := srcutil.UnixTime(float64(ms) / 1000)
	return &t
}

func partBlocks(data map[string]any) []model.Block {
	switch data["type"] {
	case "text":
		if srcutil.Truthy(data["text"]) {
			return []model.Block{{Type: model.BlockText, Text: asString(data["text"])}}
		}
	case "reasoning":
		var text *string
		if srcutil.Truthy(data["text"]) {
			text = asString(data["text"])
		}
		timeInfo := srcutil.Map(data, "time")
		payload := map[string]any{}
		if v := timeInfo["start"]; v != nil {
			payload["start_ms"] = v
		}
		if v := timeInfo["end"]; v != nil {
			payload["end_ms"] = v
		}
		return []model.Block{{Type: model.BlockThinking, Text: text, Payload: payload}}
	case "patch":
		return []model.Block{{
			Type:     model.BlockToolResult,
			ToolName: model.Ptr("patch"),
			Payload:  map[string]any{"hash": data["hash"], "files": srcutil.GetOr(data, "files", []any{})},
		}}
	}
	return nil
}

// toolMessages turns a tool part into a tool-use message and, once the tool
// has completed, a tool-result message.
func toolMessages(data map[string]any, msgModel *string, ts *time.Time) []model.Message {
	state := srcutil.Map(data, "state")
	callID := asString(data["callID"])
	status := state["status"]
	out := []model.Message{{
		SourceNativeID: callID,
		Role:           model.RoleAssistant,
		Model:          msgModel,
		CreatedAt:      ts,
		Blocks: []model.Block{{
			Type:      model.BlockToolUse,
			ToolName:  asString(data["tool"]),
			ToolUseID: callID,
			Payload:   map[string]any{"input": srcutil.GetOr(state, "input", map[string]any{}), "status": status},
		}},
		Metadata: map[string]any{},
	}}
	if status != "completed" {
		return out
	}
	output := state["output"]
	if output == nil {
		output = srcutil.Map(state, "metadata")["output"]
	}
	return append(out, model.Message{
		SourceNativeID: callID,
		Role:           model.RoleTool,
		CreatedAt:      ts,
		Blocks: []model.Block{{
			Type:      model.BlockToolResult,
			ToolUseID: callID,
			Text:      asString(output),
			Payload:   map[string]any{"status": status},
		}},
		Metadata: map[string]any{},
	})
}
