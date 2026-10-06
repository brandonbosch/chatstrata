// Package omp reads Oh My Pi session files.
//
// Oh My Pi writes one JSONL file per session to
// ~/.omp/agent/sessions/<encoded-cwd>/<timestamp>_<session-id>.jsonl. A file
// starts with a fixed-width title slot that is rewritten in place, then a
// session header and the session's entries. This is a port of
// chatstrata/sources/omp/adapter.py and must produce the same archive (see
// spec/golden).
package omp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/pystr"
	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// DefaultRoot is where Oh My Pi keeps sessions, relative to $HOME.
const DefaultRoot = ".omp/agent/sessions"

type Source struct{}

func (Source) Name() string        { return "omp" }
func (Source) DisplayName() string { return "Oh My Pi" }
func (Source) Version() string     { return "0.1.0" }

// AppendOnly is false: the title slot at the start of a file is rewritten in
// place, so a changed file is logged as a snapshot.
func (Source) AppendOnly() bool { return false }

var roles = map[string]model.Role{
	"user":       model.RoleUser,
	"assistant":  model.RoleAssistant,
	"toolResult": model.RoleTool,
}

// Discover lists root/*/*.jsonl, one handle per session file.
func (Source) Discover(root string) ([]model.Handle, error) {
	root, err := srcutil.Root(root, DefaultRoot)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("list sessions in %s: %w", root, err)
	}
	sort.Strings(paths)
	handles := make([]model.Handle, 0, len(paths))
	for _, p := range paths {
		stem := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		// <timestamp>_<session-id>: the id follows the first underscore.
		id := stem
		if _, after, ok := strings.Cut(stem, "_"); ok {
			id = after
		}
		handles = append(handles, model.Handle{
			SourceNativeID: id,
			Path:           p,
			Project:        decodeProjectDir(filepath.Base(filepath.Dir(p))),
		})
	}
	return handles, nil
}

// decodeProjectDir is a lossy backstop for the cwd bucket name: buckets under
// home are "-<relative>", absolute ones "--<encoded>--". Prefer the cwd in
// the session header.
func decodeProjectDir(name string) string {
	if strings.HasPrefix(name, "--") && strings.HasSuffix(name, "--") {
		inner := ""
		if len(name) > 4 {
			inner = name[2 : len(name)-2]
		}
		return "/" + strings.ReplaceAll(inner, "-", "/")
	}
	if rel, ok := strings.CutPrefix(name, "-"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return name
		}
		return filepath.Join(home, strings.ReplaceAll(rel, "-", "/"))
	}
	return name
}

// Parse reads one session file into a conversation.
func (Source) Parse(h model.Handle, content []byte) (*model.Conversation, error) {
	events := srcutil.ReadJSONL(content)
	conv := &model.Conversation{
		SourceNativeID: h.SourceNativeID,
		RawPath:        h.Path,
		Metadata:       map[string]any{"event_count": len(events)},
		RawEvents:      make([]json.RawMessage, 0, len(events)),
	}
	var span srcutil.Span
	for _, ev := range events {
		conv.RawEvents = append(conv.RawEvents, ev.Raw)
		f := ev.Fields
		switch f["type"] {
		case "title":
			// The fixed-width title slot; the header's title wins over it.
			if conv.Title == nil {
				conv.Title = strippedTitle(f)
			}
		case "session":
			if t := strippedTitle(f); t != nil {
				conv.Title = t
			}
			if cwd := srcutil.Str(f, "cwd"); cwd != nil {
				conv.Project = cwd
			}
			if ts := srcutil.ISOTime(f["timestamp"]); ts != nil {
				span.Start = ts
			}
		case "message":
			message := srcutil.Map(f, "message")
			roleName, ok := message["role"].(string)
			role, known := roles[roleName]
			if !ok || !known {
				// Other roles (developer, bashExecution, hook payloads, ...)
				// carry no conversation.
				continue
			}
			var blocks []model.Block
			if roleName == "toolResult" {
				blocks = toolResultBlocks(message)
			} else {
				blocks = contentBlocks(message["content"])
			}
			if len(blocks) == 0 {
				continue
			}
			ts := srcutil.ISOTime(f["timestamp"])
			span.Add(ts)
			metadata := map[string]any{}
			if a, ok := message["attribution"].(string); ok {
				metadata["attribution"] = a
			}
			conv.Messages = append(conv.Messages, model.Message{
				SourceNativeID:       srcutil.Str(f, "id"),
				ParentSourceNativeID: srcutil.Str(f, "parentId"),
				Role:                 role,
				Model:                srcutil.Str(message, "model"),
				CreatedAt:            ts,
				Blocks:               blocks,
				Metadata:             metadata,
			})
		case "custom_message":
			// Extension messages that take part in the transcript.
			text := textOf(f["content"])
			if text == nil || *text == "" {
				continue
			}
			role := model.RoleAssistant
			metadata := map[string]any{}
			if a, ok := f["attribution"].(string); ok {
				metadata["attribution"] = a
				if a == "user" {
					role = model.RoleUser
				}
			}
			ts := srcutil.ISOTime(f["timestamp"])
			span.Add(ts)
			conv.Messages = append(conv.Messages, model.Message{
				SourceNativeID:       srcutil.Str(f, "id"),
				ParentSourceNativeID: srcutil.Str(f, "parentId"),
				Role:                 role,
				CreatedAt:            ts,
				Blocks:               []model.Block{{Type: model.BlockText, Text: text}},
				Metadata:             metadata,
			})
		}
		// Other entry types (compaction, model_change, ...) change replay
		// state, not the transcript.
	}
	conv.StartedAt, conv.EndedAt = span.Start, span.End
	if conv.Project == nil && h.Project != "" {
		conv.Project = model.Ptr(h.Project)
	}
	if conv.Title == nil {
		conv.Title = srcutil.FirstUserLine(conv.Messages)
	}
	return conv, nil
}

func strippedTitle(f map[string]any) *string {
	if s, ok := f["title"].(string); ok {
		if t := pystr.Strip(s); t != "" {
			return &t
		}
	}
	return nil
}

// textOf joins the text blocks of a content value, or returns a string as is.
func textOf(content any) *string {
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
			if s, ok := m["text"].(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return model.Ptr(strings.Join(parts, "\n"))
		}
	}
	return nil
}

func contentBlocks(content any) []model.Block {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []model.Block{{Type: model.BlockText, Text: &c}}
	case []any:
		var blocks []model.Block
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				if s := srcutil.NonEmpty(m, "text"); s != nil {
					blocks = append(blocks, model.Block{Type: model.BlockText, Text: s})
				}
			case "thinking":
				if s := srcutil.NonEmpty(m, "thinking"); s != nil {
					blocks = append(blocks, model.Block{Type: model.BlockThinking, Text: s})
				}
			case "toolCall":
				args := m["arguments"]
				if s, ok := args.(string); ok {
					if parsed, ok := decodeAny(s); ok {
						args = parsed
					} else {
						args = map[string]any{"raw": s}
					}
				}
				blocks = append(blocks, model.Block{
					Type:      model.BlockToolUse,
					ToolName:  srcutil.Str(m, "name"),
					ToolUseID: srcutil.Str(m, "id"),
					Payload:   map[string]any{"arguments": args},
				})
			case "image":
				payload := map[string]any{}
				for k, v := range m {
					if k != "type" {
						payload[k] = v
					}
				}
				blocks = append(blocks, model.Block{Type: model.BlockImage, Payload: payload})
			}
		}
		return blocks
	}
	return nil
}

func decodeAny(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.More() {
		return nil, false
	}
	return v, true
}

func toolResultBlocks(message map[string]any) []model.Block {
	payload := map[string]any{}
	if details, ok := message["details"].(map[string]any); ok {
		payload["details"] = details
	}
	if srcutil.Truthy(message["isError"]) {
		payload["isError"] = true
	}
	return []model.Block{{
		Type:      model.BlockToolResult,
		ToolName:  srcutil.Str(message, "toolName"),
		ToolUseID: srcutil.Str(message, "toolCallId"),
		Text:      textOf(message["content"]),
		Payload:   payload,
	}}
}
