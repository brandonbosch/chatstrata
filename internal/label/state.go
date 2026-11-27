package label

import (
	"encoding/json"
	"fmt"
)

// BuildState maps one target_sql result row into the state JSON object sent
// to the classifier (before egress redaction).
func (p *Pack) BuildState(row map[string]any) map[string]any {
	out := make(map[string]any, len(p.State))
	for fieldName, spec := range p.State {
		v := row[spec.Column]
		if spec.Unwrap {
			v = unwrapEnvelope(v)
		}
		if spec.JSON {
			v = toJSONString(v, spec.Clip)
		} else if spec.Clip > 0 {
			if s, ok := asString(v); ok {
				v = clipText(s, spec.Clip)
			}
		}
		if isBlank(v) {
			if spec.Default != "" {
				out[fieldName] = spec.Default
			}
			continue
		}
		out[fieldName] = v
	}
	return out
}

func unwrapEnvelope(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := asString(v); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			v = parsed
		}
	}
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for k, inner := range m {
			if k == "input" || k == "arguments" {
				return inner
			}
		}
	}
	return v
}

func toJSONString(v any, limit int) any {
	if v == nil {
		return nil
	}
	if s, ok := asString(v); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			v = parsed
		} else if limit > 0 {
			return clipText(s, limit)
		} else {
			return s
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if limit > 0 {
		return clipText(s, limit)
	}
	return s
}

func asString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	default:
		return "", false
	}
}

func isBlank(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := asString(v); ok {
		return s == ""
	}
	return false
}

// clipText trims text to limit runes, preserving 60% head and 40% tail so both
// opening context and trailing errors survive.
func clipText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit || limit <= 0 {
		return text
	}
	head := (limit * 6) / 10
	tail := limit - head
	omitted := len(runes) - head - tail
	return fmt.Sprintf("%s\n[... %d chars omitted ...]\n%s", string(runes[:head]), omitted, string(runes[len(runes)-tail:]))
}
