package pystr

import (
	"testing"
	"time"
)

func TestStrip(t *testing.T) {
	cases := map[string]string{
		"  hi  ":            "hi",
		"\t\nhi\r\n":        "hi",
		"\x1chi\x1f":        "hi", // Python treats file/group/record/unit separators as space
		"\u00a0hi\u3000":    "hi",
		"   ":               "",
		"a b":               "a b",
		"\u200bzero-width ": "\u200bzero-width", // U+200B is not whitespace in Python
	}
	for in, want := range cases {
		if got := Strip(in); got != want {
			t.Errorf("Strip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"one\ntwo":     "one",
		"one\r\ntwo":   "one",
		"one\u2028two": "one",
		"one\x1etwo":   "one",
		"single":       "single",
	}
	for in, want := range cases {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrefixCountsCharacters(t *testing.T) {
	if got := Prefix("héllo wörld", 7); got != "héllo w" {
		t.Errorf("Prefix = %q", got)
	}
	if got := Prefix("short", 200); got != "short" {
		t.Errorf("Prefix = %q", got)
	}
}

func TestParseISOTime(t *testing.T) {
	cases := map[string]time.Time{
		"2026-03-14T10:00:05Z":           time.Date(2026, 3, 14, 10, 0, 5, 0, time.UTC),
		"2026-03-14T10:00:05.123Z":       time.Date(2026, 3, 14, 10, 0, 5, 123_000_000, time.UTC),
		"2026-03-14T12:00:05+02:00":      time.Date(2026, 3, 14, 10, 0, 5, 0, time.UTC),
		"2026-03-14T10:00:05":            time.Date(2026, 3, 14, 10, 0, 5, 0, time.UTC),
		"2026-03-14 10:00:05.5":          time.Date(2026, 3, 14, 10, 0, 5, 500_000_000, time.UTC),
		"2026-03-14":                     time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC),
		"2026-03-14T10:00:05.123456789Z": time.Date(2026, 3, 14, 10, 0, 5, 123_456_789, time.UTC),
	}
	for in, want := range cases {
		got, ok := ParseISOTime(in)
		if !ok || !got.Equal(want) {
			t.Errorf("ParseISOTime(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "14/03/2026"} {
		if _, ok := ParseISOTime(bad); ok {
			t.Errorf("ParseISOTime(%q) succeeded", bad)
		}
	}
}
