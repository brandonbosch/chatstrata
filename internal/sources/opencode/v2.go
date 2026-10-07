package opencode

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// OpenCode 2.x keeps a session in one session_v2 row and its turns in
// session_message, one self-contained JSON document per row: the turn's kind
// is the row's type, and an assistant turn's reasoning, text and tool calls
// (with their results) are entries of its content array. There is no
// Python adapter for this layout; spec/golden/captures/opencode_v2 holds the
// real session the fixture was built from.

// documentV2 is what the log holds for one OpenCode 2.x session.
type documentV2 struct {
	Session  srcutil.Row   `json:"session_v2"`
	Messages []srcutil.Row `json:"session_message"`
}

func discoverV2(ctx context.Context, db *sql.DB, path string) ([]model.Handle, error) {
	sessions, err := srcutil.QueryRows(ctx, db, "SELECT * FROM session_v2 ORDER BY time_created, id")
	if err != nil {
		return nil, fmt.Errorf("read sessions from %s: %w", path, err)
	}
	handles := make([]model.Handle, 0, len(sessions))
	for _, s := range sessions {
		id := fmt.Sprint(s.Get("id"))
		doc := documentV2{Session: s}
		if doc.Messages, err = srcutil.QueryRows(ctx, db,
			"SELECT * FROM session_message WHERE session_id = ? ORDER BY seq", id); err != nil {
			return nil, fmt.Errorf("read messages of %s: %w", id, err)
		}
		content, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		handles = append(handles, model.Handle{SourceNativeID: id, Path: path, Content: content})
	}
	return handles, nil
}

// parseV2 parses an OpenCode 2.x session document. ok is false when content
// isn't one, so the 1.x parser takes it.
func parseV2(h model.Handle, content []byte) (conv *model.Conversation, ok bool, err error) {
	top, isObj := srcutil.DecodeObject(bytes.TrimSpace(content))
	if !isObj || top["session_v2"] == nil {
		return nil, false, nil
	}
	session, _ := top["session_v2"].(map[string]any)
	rows, _ := top["session_message"].([]any)

	sessionModel := parseData(session["model"])
	conv = &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		Title:          asString(session["title"]),
		Project:        asString(session["directory"]),
		RawPath:        h.Path,
		Metadata:       map[string]any{"schema": "v2"},
	}
	if raw, err := json.Marshal(session); err == nil {
		conv.RawEvents = append(conv.RawEvents, raw)
	}

	var span srcutil.Span
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		data := parseData(row["data"])
		// Keep the row with its data decoded, so raw_events stay queryable.
		event := map[string]any{}
		for k, v := range row {
			event[k] = v
		}
		event["data"] = data
		raw, err := json.Marshal(event)
		if err != nil {
			return nil, true, err
		}
		conv.RawEvents = append(conv.RawEvents, raw)

		id := fmt.Sprint(row["id"])
		timeInfo := srcutil.Map(data, "time")
		ts := unixMillis(orAny(timeInfo["created"], row["time_created"]))
		span.Add(ts)
		if done := unixMillis(timeInfo["completed"]); done != nil && (span.End == nil || done.After(*span.End)) {
			span.End = done
		}

		switch row["type"] {
		case "user", "system":
			role := model.RoleUser
			if row["type"] == "system" {
				role = model.RoleSystem
			}
			var blocks []model.Block
			if srcutil.Truthy(data["text"]) {
				blocks = append(blocks, model.Block{Type: model.BlockText, Text: asString(data["text"])})
			}
			files, _ := data["files"].([]any)
			for _, f := range files {
				blocks = append(blocks, attachment(f))
			}
			if len(blocks) == 0 {
				continue
			}
			conv.Messages = append(conv.Messages, model.Message{
				SourceNativeID: model.Ptr(id),
				Role:           role,
				CreatedAt:      ts,
				Blocks:         blocks,
				Metadata:       map[string]any{},
			})
		case "assistant":
			conv.Messages = append(conv.Messages, assistantV2(id, data, sessionModel, ts)...)
		}
		// Other types (idle, agent-switched, model-switched, shell,
		// synthetic, ...) are harness bookkeeping, kept in raw_events only.
	}
	conv.StartedAt, conv.EndedAt = span.Start, span.End
	if conv.Title == nil || *conv.Title == "" {
		if line := srcutil.FirstUserLine(conv.Messages); line != nil {
			conv.Title = line
		}
	}
	return conv, true, nil
}

func attachment(f any) model.Block {
	b := model.Block{Type: model.BlockAttachment, Payload: map[string]any{"file": f}}
	if m, ok := f.(map[string]any); ok {
		for _, k := range []string{"filename", "name", "path", "url"} {
			if s := srcutil.NonEmpty(m, k); s != nil {
				b.Text = s
				break
			}
		}
	}
	return b
}

// assistantV2 splits an assistant turn the way the 1.x parser splits parts:
// text and reasoning become one message, and each tool call becomes a
// tool-use message followed, once it has finished, by its result.
func assistantV2(id string, data, sessionModel map[string]any, ts *time.Time) []model.Message {
	info, _ := data["model"].(map[string]any)
	if info == nil {
		info = sessionModel
	}
	msgModel := asString(info["id"])
	metadata := map[string]any{}
	if srcutil.Truthy(info["providerID"]) {
		metadata["provider_id"] = info["providerID"]
	}
	for _, k := range []string{"agent", "mode", "tokens"} {
		if srcutil.Truthy(data[k]) {
			metadata[k] = data[k]
		}
	}

	var out []model.Message
	var pending []model.Block
	flush := func() {
		if len(pending) == 0 {
			return
		}
		out = append(out, model.Message{
			SourceNativeID: model.Ptr(id),
			Role:           model.RoleAssistant,
			Model:          msgModel,
			CreatedAt:      ts,
			Blocks:         pending,
			Metadata:       metadata,
		})
		pending = nil
	}
	items, _ := data["content"].([]any)
	for _, it := range items {
		item, ok := it.(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "text":
			if srcutil.Truthy(item["text"]) {
				pending = append(pending, model.Block{Type: model.BlockText, Text: asString(item["text"])})
			}
		case "reasoning":
			var text *string
			if srcutil.Truthy(item["text"]) {
				text = asString(item["text"])
			}
			timeInfo := srcutil.Map(item, "time")
			payload := map[string]any{}
			if v := timeInfo["created"]; v != nil {
				payload["start_ms"] = v
			}
			if v := timeInfo["completed"]; v != nil {
				payload["end_ms"] = v
			}
			pending = append(pending, model.Block{Type: model.BlockThinking, Text: text, Payload: payload})
		case "tool":
			flush()
			out = append(out, toolV2(item, msgModel, ts)...)
		case "patch":
			pending = append(pending, partBlocks(item)...)
		}
	}
	flush()
	return out
}

// toolV2 turns a tool entry into a tool-use message and, once the tool has
// completed or failed, a tool-result message.
func toolV2(item map[string]any, msgModel *string, ts *time.Time) []model.Message {
	state := srcutil.Map(item, "state")
	callID := asString(item["id"])
	status := state["status"]
	out := []model.Message{{
		SourceNativeID: callID,
		Role:           model.RoleAssistant,
		Model:          msgModel,
		CreatedAt:      ts,
		Blocks: []model.Block{{
			Type:      model.BlockToolUse,
			ToolName:  asString(item["name"]),
			ToolUseID: callID,
			Payload:   map[string]any{"input": srcutil.GetOr(state, "input", map[string]any{}), "status": status},
		}},
		Metadata: map[string]any{},
	}}
	if status != "completed" && status != "error" {
		return out
	}
	payload := map[string]any{"status": status}
	if status == "error" {
		payload["is_error"] = true
	}
	return append(out, model.Message{
		SourceNativeID: callID,
		Role:           model.RoleTool,
		CreatedAt:      ts,
		Blocks: []model.Block{{
			Type:      model.BlockToolResult,
			ToolUseID: callID,
			Text:      toolOutput(state),
			Payload:   payload,
		}},
		Metadata: map[string]any{},
	})
}

// toolOutput is the tool's result text: its text content, else the output
// recorded in metadata, else an error message.
func toolOutput(state map[string]any) *string {
	if items, ok := state["content"].([]any); ok {
		var parts []string
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		if len(parts) > 0 {
			return model.Ptr(strings.Join(parts, "\n"))
		}
	}
	for _, v := range []any{state["output"], srcutil.Map(state, "metadata")["output"], state["error"]} {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	return nil
}
