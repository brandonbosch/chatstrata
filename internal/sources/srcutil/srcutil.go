// Package srcutil holds the small helpers the source adapters share: reading
// JSONL, Python-style dict access, timestamps and title fallbacks.
package srcutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/pystr"
)

// Event is one JSONL record: its original bytes and its decoded fields.
type Event struct {
	Raw    json.RawMessage
	Fields map[string]any
}

// ReadJSONL decodes one JSON object per line, as the Python adapters do:
// lines are stripped, blank ones ignored, and malformed ones (or ones that
// aren't objects) skipped so one bad line can't sink a session. Numbers stay
// json.Number so they survive unchanged.
func ReadJSONL(content []byte) []Event {
	var events []Event
	for len(content) > 0 {
		line := content
		if i := bytes.IndexByte(content, '\n'); i >= 0 {
			line, content = content[:i], content[i+1:]
		} else {
			content = nil
		}
		trimmed := bytes.TrimFunc(line, pystr.IsSpace)
		if len(trimmed) == 0 {
			continue
		}
		if fields, ok := DecodeObject(trimmed); ok {
			events = append(events, Event{Raw: json.RawMessage(bytes.Clone(trimmed)), Fields: fields})
		}
	}
	return events
}

// DecodeObject decodes b as a single JSON object, keeping numbers as
// json.Number.
func DecodeObject(b []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fields map[string]any
	if dec.Decode(&fields) != nil || fields == nil || dec.More() {
		return nil, false
	}
	return fields, true
}

// Str is m.get(key) when it is a string.
func Str(m map[string]any, key string) *string {
	if s, ok := m[key].(string); ok {
		return &s
	}
	return nil
}

// NonEmpty is Str, but only for a non-empty string (Python truthiness).
func NonEmpty(m map[string]any, key string) *string {
	if s, ok := m[key].(string); ok && s != "" {
		return &s
	}
	return nil
}

// GetOr is Python's dict.get(key, default): a present null stays null.
func GetOr(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// Map is m[key] when it is an object, else nil (which reads as empty).
func Map(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

// Truthy is Python's bool(v) for decoded JSON.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case float64:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// ISOTime parses an ISO 8601 string field the way the Python adapters do.
func ISOTime(v any) *time.Time {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	if t, ok := pystr.ParseISOTime(s); ok {
		return &t
	}
	return nil
}

// UnixTime is datetime.fromtimestamp(seconds, tz=utc), rounded to the
// microsecond as Python rounds it.
func UnixTime(seconds float64) time.Time {
	micros := int64(roundHalfEven(seconds * 1e6))
	return time.UnixMicro(micros).UTC()
}

func roundHalfEven(x float64) float64 {
	r := float64(int64(x))
	diff := x - r
	switch {
	case diff > 0.5 || (diff == 0.5 && int64(r)%2 != 0):
		r++
	case diff < -0.5 || (diff == -0.5 && int64(r)%2 != 0):
		r--
	}
	return r
}

// Span tracks the earliest and latest timestamps seen.
type Span struct{ Start, End *time.Time }

func (s *Span) Add(t *time.Time) {
	if t == nil {
		return
	}
	if s.Start == nil || t.Before(*s.Start) {
		s.Start = t
	}
	if s.End == nil || t.After(*s.End) {
		s.End = t
	}
}

// FirstUserLine is the Python adapters' title fallback: the first line of the
// first non-empty text block of the first user message that has one, capped
// at 200 characters.
func FirstUserLine(messages []model.Message) *string {
	for _, m := range messages {
		if m.Role != model.RoleUser {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type != model.BlockText || b.Text == nil || *b.Text == "" {
				continue
			}
			if line := pystr.Prefix(pystr.FirstLine(pystr.Strip(*b.Text)), 200); line != "" {
				return &line
			}
			break
		}
	}
	return nil
}

// Root is the directory or file a source reads: the --path override, or the
// default under $HOME.
func Root(override, defaultRel string) (string, error) {
	if override != "" {
		return ExpandHome(override)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, defaultRel), nil
}

// ExpandHome expands a leading ~/ the way Path.expanduser does.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
