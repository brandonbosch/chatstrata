// Package claudeexport reads the claude.ai data export (Settings → Account →
// Export data).
//
// The export's conversations.json is one JSON array holding every
// conversation, so each conversation's bytes come from Discover (the array
// element, exactly as exported) rather than from a file of its own. There is
// no default location; pass the file or the unzipped export directory. This
// is a port of chatstrata/sources/claude_export/adapter.py and must produce
// the same archive (see spec/golden).
package claudeexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

type Source struct{}

func (Source) Name() string        { return "claude_export" }
func (Source) DisplayName() string { return "Claude Export" }
func (Source) Version() string     { return "0.1.0" }

// AppendOnly is false: each conversation is logged whole when it changes.
func (Source) AppendOnly() bool { return false }

// resolve finds conversations.json from a file or export directory path.
func resolve(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	path, err := srcutil.ExpandHome(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", nil
	}
	if !fi.IsDir() {
		return path, nil
	}
	candidate := filepath.Join(path, "conversations.json")
	if _, err := os.Stat(candidate); err != nil {
		return "", nil
	}
	return candidate, nil
}

// Discover reads conversations.json and returns one handle per conversation,
// carrying that conversation's bytes.
func (Source) Discover(root string) ([]model.Handle, error) {
	path, err := resolve(root)
	if err != nil || path == "" {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("%s is not a claude.ai export (expected a JSON array)", path)
	}
	var handles []model.Handle
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var head struct {
			UUID any `json:"uuid"`
		}
		if json.Unmarshal(raw, &head) != nil {
			continue // not an object
		}
		id, ok := head.UUID.(string)
		if !ok || id == "" {
			continue
		}
		handles = append(handles, model.Handle{SourceNativeID: id, Path: path, Content: raw})
	}
	return handles, nil
}

// Parse reads one exported conversation.
func (Source) Parse(h model.Handle, content []byte) (*model.Conversation, error) {
	conv, ok := srcutil.DecodeObject(bytes.TrimSpace(content))
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	chat, _ := conv["chat_messages"].([]any)
	out := &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		Title:          srcutil.Str(conv, "name"),
		RawPath:        h.Path,
		Metadata:       map[string]any{"message_count": len(chat)},
		RawEvents:      []json.RawMessage{json.RawMessage(bytes.TrimSpace(content))},
	}
	span := srcutil.Span{Start: srcutil.ISOTime(conv["created_at"]), End: srcutil.ISOTime(conv["updated_at"])}
	for _, item := range chat {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blocks := messageBlocks(msg)
		if len(blocks) == 0 {
			continue
		}
		ts := srcutil.ISOTime(msg["created_at"])
		span.Add(ts)
		role := model.RoleAssistant
		if srcutil.GetOr(msg, "sender", "human") == "human" {
			role = model.RoleUser
		}
		metadata := map[string]any{}
		if s, ok := msg["sender"]; ok && s != nil {
			metadata["sender"] = s
		}
		out.Messages = append(out.Messages, model.Message{
			SourceNativeID: srcutil.Str(msg, "uuid"),
			Role:           role,
			CreatedAt:      ts,
			Blocks:         blocks,
			Metadata:       metadata,
		})
	}
	out.StartedAt, out.EndedAt = span.Start, span.End
	if out.Title == nil {
		out.Title = srcutil.FirstUserLine(out.Messages)
	}
	return out, nil
}

func messageBlocks(msg map[string]any) []model.Block {
	var blocks []model.Block
	if content, ok := msg["content"].([]any); ok && len(content) > 0 {
		for _, item := range content {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blocks = append(blocks, convertBlock(raw))
		}
	} else if srcutil.Truthy(msg["text"]) {
		blocks = append(blocks, model.Block{Type: model.BlockText, Text: srcutil.Str(msg, "text")})
	}

	attachments, _ := msg["attachments"].([]any)
	for _, item := range attachments {
		att, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blocks = append(blocks, model.Block{
			Type: model.BlockAttachment,
			Text: srcutil.Str(att, "file_name"),
			Payload: map[string]any{
				"file_name":         att["file_name"],
				"file_size":         att["file_size"],
				"file_type":         att["file_type"],
				"extracted_content": att["extracted_content"],
			},
		})
	}
	return blocks
}

func convertBlock(raw map[string]any) model.Block {
	switch raw["type"] {
	case "text":
		return model.Block{Type: model.BlockText, Text: srcutil.Str(raw, "text")}
	case "thinking":
		return model.Block{Type: model.BlockThinking, Text: srcutil.Str(raw, "thinking")}
	case "tool_use":
		return model.Block{
			Type:      model.BlockToolUse,
			ToolName:  srcutil.Str(raw, "name"),
			ToolUseID: srcutil.Str(raw, "id"),
			Payload:   map[string]any{"input": srcutil.GetOr(raw, "input", map[string]any{})},
		}
	case "tool_result":
		result := raw["content"]
		return model.Block{
			Type:      model.BlockToolResult,
			ToolUseID: srcutil.Str(raw, "tool_use_id"),
			Text:      resultText(result),
			Payload: map[string]any{
				"is_error":    srcutil.GetOr(raw, "is_error", false),
				"raw_content": result,
			},
		}
	case "image":
		return model.Block{
			Type:    model.BlockImage,
			Payload: map[string]any{"source": srcutil.GetOr(raw, "source", map[string]any{})},
		}
	}
	return model.Block{Type: model.BlockText, Payload: map[string]any{"unknown_block": raw}}
}

func resultText(content any) *string {
	switch c := content.(type) {
	case string:
		return &c
	case []any:
		var parts []string
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok || m["type"] != "text" {
				continue
			}
			s, _ := srcutil.GetOr(m, "text", "").(string)
			parts = append(parts, s)
		}
		if len(parts) > 0 {
			return model.Ptr(strings.Join(parts, "\n"))
		}
	}
	return nil
}
