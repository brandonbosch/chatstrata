// Package codexcli reads OpenAI Codex CLI rollout files.
//
// Codex writes one JSONL rollout per session to
// ~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-uuid>.jsonl. Each
// line is an envelope {timestamp, type, payload}; the conversation is in the
// response_item and event_msg.user_message records. This is a port of
// chatstrata/sources/codex_cli/adapter.py and must produce the same archive
// (see spec/golden).
package codexcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// DefaultRoot is where Codex keeps rollouts, relative to $HOME.
const DefaultRoot = ".codex/sessions"

type Source struct{}

func (Source) Name() string        { return "codex_cli" }
func (Source) DisplayName() string { return "Codex CLI" }
func (Source) Version() string     { return "0.1.0" }

// AppendOnly is true: Codex appends to a rollout and never rewrites it.
func (Source) AppendOnly() bool { return true }

// Discover lists every rollout-*.jsonl under root, at any depth.
func (Source) Discover(root string) ([]model.Handle, error) {
	root, err := srcutil.Root(root, DefaultRoot)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list rollouts in %s: %w", root, err)
	}
	sort.Strings(paths)
	handles := make([]model.Handle, 0, len(paths))
	for _, p := range paths {
		handles = append(handles, model.Handle{SourceNativeID: sessionID(filepath.Base(p)), Path: p})
	}
	return handles, nil
}

// sessionID takes the trailing UUID (five dash-separated groups) from a
// rollout-<timestamp>-<uuid>.jsonl name.
func sessionID(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	rest, ok := strings.CutPrefix(stem, "rollout-")
	if !ok {
		return stem
	}
	parts := strings.Split(rest, "-")
	if len(parts) >= 5 {
		return strings.Join(parts[len(parts)-5:], "-")
	}
	return rest
}

// timestamp accepts ISO strings and Unix seconds.
func timestamp(v any) *time.Time {
	if n, ok := v.(json.Number); ok {
		f, err := n.Float64()
		if err != nil {
			return nil
		}
		t := srcutil.UnixTime(f)
		return &t
	}
	return srcutil.ISOTime(v)
}

// Parse reads one rollout into a conversation.
func (Source) Parse(h model.Handle, content []byte) (*model.Conversation, error) {
	events := srcutil.ReadJSONL(content)
	conv := &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		RawPath:        h.Path,
		Metadata:       map[string]any{"event_count": len(events)},
		RawEvents:      make([]json.RawMessage, 0, len(events)),
	}
	var span srcutil.Span
	var modelName *string
	for _, ev := range events {
		conv.RawEvents = append(conv.RawEvents, ev.Raw)
		ts := timestamp(ev.Fields["timestamp"])
		span.Add(ts)
		payload := srcutil.Map(ev.Fields, "payload")

		switch ev.Fields["type"] {
		case "session_meta":
			if conv.Project == nil {
				conv.Project = srcutil.NonEmpty(payload, "cwd")
			}
		case "turn_context":
			if modelName == nil {
				modelName = srcutil.NonEmpty(payload, "model")
			}
			if conv.Project == nil {
				conv.Project = srcutil.NonEmpty(payload, "cwd")
			}
		case "event_msg":
			if payload["type"] != "user_message" {
				continue
			}
			if text := srcutil.NonEmpty(payload, "message"); text != nil {
				conv.Messages = append(conv.Messages, model.Message{
					Role:      model.RoleUser,
					CreatedAt: ts,
					Blocks:    []model.Block{{Type: model.BlockText, Text: text}},
					Metadata:  map[string]any{},
				})
			}
		case "response_item":
			role, blocks := responseBlocks(payload)
			if role == "" || len(blocks) == 0 {
				continue
			}
			msg := model.Message{
				SourceNativeID: srcutil.NonEmpty(payload, "call_id"),
				Role:           role,
				CreatedAt:      ts,
				Blocks:         blocks,
				Metadata:       map[string]any{},
			}
			if msg.SourceNativeID == nil {
				msg.SourceNativeID = srcutil.NonEmpty(payload, "id")
			}
			if role == model.RoleAssistant {
				msg.Model = modelName
			}
			conv.Messages = append(conv.Messages, msg)
		}
	}
	conv.StartedAt, conv.EndedAt = span.Start, span.End
	conv.Title = srcutil.FirstUserLine(conv.Messages)
	return conv, nil
}

// joinText joins the non-empty text fields of a content array.
func joinText(content any) *string {
	items, ok := content.([]any)
	if !ok {
		return nil
	}
	var parts []string
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			if srcutil.Truthy(m["text"]) {
				parts = append(parts, fmt.Sprint(m["text"]))
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return model.Ptr(strings.Join(parts, "\n"))
}

// outputText normalizes tool output, which newer rollouts encode as a
// content array.
func outputText(v any) *string {
	switch x := v.(type) {
	case string:
		return &x
	case []any:
		return joinText(x)
	case map[string]any:
		return srcutil.Str(x, "text")
	}
	return nil
}

func responseBlocks(p map[string]any) (model.Role, []model.Block) {
	switch p["type"] {
	case "message":
		text := joinText(srcutil.GetOr(p, "content", []any{}))
		if text == nil {
			return "", nil
		}
		switch p["role"] {
		case "assistant":
			return model.RoleAssistant, []model.Block{{Type: model.BlockText, Text: text}}
		case "user":
			return model.RoleUser, []model.Block{{Type: model.BlockText, Text: text}}
		}
		// developer messages are system prompts, not conversation.
		return "", nil
	case "reasoning":
		// Reasoning with an empty summary is still thinking (its content is
		// encrypted).
		return model.RoleAssistant, []model.Block{{
			Type:    model.BlockThinking,
			Text:    joinText(srcutil.GetOr(p, "summary", []any{})),
			Payload: map[string]any{"has_encrypted_content": srcutil.Truthy(p["encrypted_content"])},
		}}
	case "function_call":
		return model.RoleAssistant, []model.Block{{
			Type:      model.BlockToolUse,
			ToolName:  srcutil.Str(p, "name"),
			ToolUseID: srcutil.Str(p, "call_id"),
			Payload:   map[string]any{"arguments": srcutil.GetOr(p, "arguments", "")},
		}}
	case "function_call_output", "custom_tool_call_output":
		return model.RoleTool, []model.Block{{
			Type:      model.BlockToolResult,
			ToolUseID: srcutil.Str(p, "call_id"),
			Text:      outputText(p["output"]),
		}}
	case "web_search_call":
		var query any
		if action, ok := srcutil.GetOr(p, "action", map[string]any{}).(map[string]any); ok {
			query = action["query"]
		}
		return model.RoleAssistant, []model.Block{{
			Type:      model.BlockToolUse,
			ToolName:  model.Ptr("web_search"),
			ToolUseID: srcutil.Str(p, "id"),
			Payload:   map[string]any{"status": p["status"], "query": query},
		}}
	case "custom_tool_call":
		return model.RoleAssistant, []model.Block{{
			Type:      model.BlockToolUse,
			ToolName:  srcutil.Str(p, "name"),
			ToolUseID: srcutil.Str(p, "call_id"),
			Payload:   map[string]any{"input": srcutil.GetOr(p, "input", "")},
		}}
	}
	return "", nil
}
