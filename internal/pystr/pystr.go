// Package pystr reproduces the Python string operations the adapters rely on,
// so titles and snippets match the Python implementation exactly.
package pystr

import (
	"strings"
	"time"
	"unicode"
)

// IsSpace reports whether r is whitespace according to Python's str.isspace.
// It differs from unicode.IsSpace only in treating U+001C..U+001F as space.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Strip is Python's str.strip() with no arguments.
func Strip(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// FirstLine returns splitlines()[0], or "" when s has no lines.
func FirstLine(s string) string {
	if i := strings.IndexFunc(s, isLineBreak); i >= 0 {
		return s[:i]
	}
	return s
}

// Prefix returns the first n code points of s, like s[:n] in Python.
func Prefix(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// ParseISOTime parses the timestamp shapes Python's datetime.fromisoformat
// accepts in practice. Timestamps without a zone are taken as UTC, matching
// how the Python ingester normalizes naive datetimes.
func ParseISOTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
