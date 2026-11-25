package redact

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrInvalidSpan means a span is out of range or empty. Callers must not
// write the target text.
var ErrInvalidSpan = errors.New("invalid redaction span")

// Span is a redaction in byte offsets of one text value. It is the storage
// form for the archive overlay: it never contains the matched bytes.
type Span struct {
	Start    int    `json:"start"`
	End      int    `json:"end"`
	RuleID   string `json:"rule_id"`
	Category string `json:"category,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// Span returns the storable form of a finding.
func (f Finding) Span() Span {
	return Span{Start: f.Start, End: f.End, RuleID: f.RuleID, Category: f.Category, Priority: f.priority}
}

// Marker returns the fixed placeholder for a category or rule ID.
func Marker(name string) string { return placeholder(name) }

// Apply replaces spans in text with typed placeholders. Spans may overlap;
// containing spans win, then priority and length. Invalid spans are an error.
func Apply(text string, spans []Span) (string, error) {
	if len(spans) == 0 {
		return text, nil
	}
	for _, s := range spans {
		if s.Start < 0 || s.End <= s.Start || s.End > len(text) {
			return "", fmt.Errorf("%w: %d..%d", ErrInvalidSpan, s.Start, s.End)
		}
	}
	merged := MergeSpans(spans)
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, s := range merged {
		if s.Start < 0 || s.End <= s.Start || s.End > len(text) {
			return "", fmt.Errorf("%w: %d..%d", ErrInvalidSpan, s.Start, s.End)
		}
		if s.Start < last {
			return "", fmt.Errorf("%w: overlapping span %d..%d", ErrInvalidSpan, s.Start, s.End)
		}
		b.WriteString(text[last:s.Start])
		b.WriteString(placeholderForSpan(s))
		last = s.End
	}
	b.WriteString(text[last:])
	return b.String(), nil
}

// MergeSpans returns non-overlapping spans in input order. A span containing
// another wins regardless of priority; otherwise priority, then length.
func MergeSpans(spans []Span) []Span {
	valid := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.Start < 0 || s.End <= s.Start {
			continue
		}
		valid = append(valid, s)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		a, b := valid[i], valid[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.End != b.End {
			return a.End > b.End
		}
		return a.RuleID < b.RuleID
	})
	out := make([]Span, 0, len(valid))
	for _, s := range valid {
		if len(out) == 0 || s.Start >= out[len(out)-1].End {
			out = append(out, s)
			continue
		}
		if betterSpan(s, out[len(out)-1]) {
			out[len(out)-1] = s
		}
	}
	return out
}

func betterSpan(a, b Span) bool {
	aContainsB := a.Start <= b.Start && a.End >= b.End
	bContainsA := b.Start <= a.Start && b.End >= a.End
	switch {
	case aContainsB && !bContainsA:
		return true
	case bContainsA && !aContainsB:
		return false
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if a.End-a.Start != b.End-b.Start {
		return a.End-a.Start > b.End-b.Start
	}
	return a.RuleID < b.RuleID
}

func placeholderForSpan(s Span) string {
	name := s.Category
	if name == "" {
		name = s.RuleID
	}
	return placeholder(name)
}

// ApplyPreserving replaces spans with length-preserving markers. It is for the
// observation log, where append offsets are byte positions: changing a
// record's length would corrupt every later append in that conversation. A
// marker that does not fit becomes '*' repeated to the span's length, so the
// category is lost for very short values.
func ApplyPreserving(text string, spans []Span) (string, error) {
	if len(spans) == 0 {
		return text, nil
	}
	for _, s := range spans {
		if s.Start < 0 || s.End <= s.Start || s.End > len(text) {
			return "", fmt.Errorf("%w: %d..%d", ErrInvalidSpan, s.Start, s.End)
		}
	}
	merged := MergeSpans(spans)
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, s := range merged {
		if s.Start < last {
			return "", fmt.Errorf("%w: overlapping span %d..%d", ErrInvalidSpan, s.Start, s.End)
		}
		b.WriteString(text[last:s.Start])
		b.WriteString(preservingMarker(s, s.End-s.Start))
		last = s.End
	}
	b.WriteString(text[last:])
	return b.String(), nil
}

func preservingMarker(s Span, width int) string {
	marker := placeholderForSpan(s)
	if len(marker) <= width {
		return marker + strings.Repeat(" ", width-len(marker))
	}
	return strings.Repeat("*", width)
}
