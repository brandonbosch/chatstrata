// Package model defines the canonical records every source adapter produces.
//
// It mirrors chatstrata/core/models.py. Optional fields are pointers so that
// "absent" (stored as NULL) and "empty" stay distinct, as they are in Python.
package model

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockThinking   BlockType = "thinking"
	BlockImage      BlockType = "image"
	BlockAttachment BlockType = "attachment"
)

// Block is one content unit within a message.
type Block struct {
	Type      BlockType
	Text      *string
	ToolName  *string
	ToolUseID *string
	// Payload is type-specific structured data. Nil or empty is stored as NULL.
	Payload map[string]any
}

// Message is one turn.
type Message struct {
	SourceNativeID       *string
	ParentSourceNativeID *string
	Role                 Role
	Model                *string
	CreatedAt            *time.Time
	Blocks               []Block
	Metadata             map[string]any
}

// Conversation is a parsed session, thread or export conversation.
type Conversation struct {
	SourceNativeID string
	Title          *string
	Project        *string
	StartedAt      *time.Time
	EndedAt        *time.Time
	Messages       []Message
	RawPath        string
	Metadata       map[string]any
	// RawEvents are the source records, one per line, as the original bytes.
	RawEvents []json.RawMessage
}

// Handle is a reference to a conversation that a source can parse later.
type Handle struct {
	SourceNativeID string
	Path           string
	// Project is the adapter's fallback project guess from the file location.
	Project string
}

// Source is implemented by every adapter.
type Source interface {
	Name() string
	DisplayName() string
	Version() string
	// Discover lists conversations under root, or the source's default
	// location when root is empty.
	Discover(root string) ([]Handle, error)
	Parse(h Handle) (*Conversation, error)
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }
