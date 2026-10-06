// Package claudecode reads Claude Code session transcripts.
//
// Claude Code writes one JSONL file per session to
// ~/.claude/projects/<encoded-cwd>/<session-uuid>.jsonl. This is a port of
// chatstrata/sources/claude_code/adapter.py and must produce the same archive
// (see spec/golden).
package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/pystr"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// DefaultRoot is where Claude Code keeps transcripts, relative to $HOME.
const DefaultRoot = ".claude/projects"

type Source struct{}

func (Source) Name() string        { return "claude_code" }
func (Source) DisplayName() string { return "Claude Code" }
func (Source) Version() string     { return "0.1.0" }

// Discover lists root/*/*.jsonl, one handle per session file.
func (Source) Discover(root string) ([]model.Handle, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate home directory: %w", err)
		}
		root = filepath.Join(home, DefaultRoot)
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("list transcripts in %s: %w", root, err)
	}
	sort.Strings(paths)
	handles := make([]model.Handle, 0, len(paths))
	for _, p := range paths {
		handles = append(handles, model.Handle{
			SourceNativeID: strings.TrimSuffix(filepath.Base(p), ".jsonl"),
			Path:           p,
			Project:        decodeProjectDir(filepath.Base(filepath.Dir(p))),
		})
	}
	return handles, nil
}

// decodeProjectDir is a lossy, last-resort decode of the folder name: Claude
// Code maps '/', '_', '-' and '.' in the cwd all to '-'. Prefer the cwd
// recorded in the transcript.
func decodeProjectDir(name string) string {
	if strings.HasPrefix(name, "-") {
		return "/" + strings.ReplaceAll(name[1:], "-", "/")
	}
	return name
}

// AppendOnly is true: Claude Code only ever appends lines to a transcript.
func (Source) AppendOnly() bool { return true }

// Parse reads one transcript into a conversation.
func (Source) Parse(h model.Handle, content []byte) (*model.Conversation, error) {
	events := srcutil.ReadJSONL(content)

	conv := &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		RawPath:        h.Path,
		Metadata:       map[string]any{"event_count": len(events)},
		RawEvents:      make([]json.RawMessage, 0, len(events)),
	}
	for _, ev := range events {
		conv.RawEvents = append(conv.RawEvents, ev.Raw)
	}

	for _, ev := range events {
		etype, _ := ev.Fields["type"].(string)
		if etype == "summary" {
			// The first summary that is a string becomes the title.
			if conv.Title == nil {
				if s, ok := ev.Fields["summary"].(string); ok {
					conv.Title = model.Ptr(pystr.Strip(s))
				}
			}
			continue
		}

		role, ok := roleFor(etype)
		if !ok {
			continue
		}
		message, _ := ev.Fields["message"].(map[string]any)
		blocks := blocksFromMessage(message)
		// Bookkeeping events without extractable content are skipped.
		if len(blocks) == 0 {
			continue
		}

		var ts *time.Time
		if s, ok := ev.Fields["timestamp"].(string); ok {
			if t, ok := pystr.ParseISOTime(s); ok {
				ts = &t
				if conv.StartedAt == nil || t.Before(*conv.StartedAt) {
					conv.StartedAt = &t
				}
				if conv.EndedAt == nil || t.After(*conv.EndedAt) {
					conv.EndedAt = &t
				}
			}
		}

		conv.Messages = append(conv.Messages, model.Message{
			SourceNativeID:       srcutil.Str(ev.Fields, "uuid"),
			ParentSourceNativeID: srcutil.Str(ev.Fields, "parentUuid"),
			Role:                 role,
			Model:                srcutil.Str(message, "model"),
			CreatedAt:            ts,
			Blocks:               blocks,
			Metadata: map[string]any{
				"request_id": ev.Fields["requestId"],
				"cwd":        ev.Fields["cwd"],
			},
		})
	}

	// The cwd recorded in the transcript is lossless; the folder name is not.
	for _, ev := range events {
		if cwd, ok := ev.Fields["cwd"].(string); ok && cwd != "" {
			conv.Project = model.Ptr(cwd)
			break
		}
	}
	if conv.Project == nil && h.Project != "" {
		conv.Project = model.Ptr(h.Project)
	}

	if conv.Title == nil {
		conv.Title = srcutil.FirstUserLine(conv.Messages)
	}
	return conv, nil
}

func roleFor(eventType string) (model.Role, bool) {
	switch eventType {
	case "user":
		return model.RoleUser, true
	case "assistant":
		return model.RoleAssistant, true
	case "system":
		return model.RoleSystem, true
	}
	return "", false
}

// blocksFromMessage converts Anthropic-shaped message content, which is either
// a plain string or a list of typed blocks.
func blocksFromMessage(message map[string]any) []model.Block {
	switch content := message["content"].(type) {
	case string:
		return []model.Block{{Type: model.BlockText, Text: &content}}
	case []any:
		var blocks []model.Block
		for _, item := range content {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blocks = append(blocks, convertBlock(raw))
		}
		return blocks
	}
	return nil
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
			Text:      toolResultText(result),
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
	// Unknown block types are kept whole for forensics.
	return model.Block{Type: model.BlockText, Payload: map[string]any{"unknown_block": raw}}
}

// toolResultText flattens a tool result: a string as-is, or the text items of
// a list joined by newlines.
func toolResultText(content any) *string {
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
			text, _ := srcutil.GetOr(m, "text", "").(string)
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			return model.Ptr(strings.Join(parts, "\n"))
		}
	}
	return nil
}
