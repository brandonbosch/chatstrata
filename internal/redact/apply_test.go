package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestApplySpans(t *testing.T) {
	text := "mail alice@example.com from /home/alice/work"
	spans := []Span{
		{Start: 5, End: 22, RuleID: "pii-email", Category: "EMAIL"},
		{Start: 34, End: 39, RuleID: "pii-home-path", Category: "USERNAME"},
	}
	got, err := Apply(text, spans)
	if err != nil {
		t.Fatal(err)
	}
	want := "mail [REDACTED:EMAIL] from /home/[REDACTED:USERNAME]/work"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

func TestApplyPrefersContainingSpan(t *testing.T) {
	text := "alice@example.com"
	spans := []Span{
		{Start: 0, End: 5, RuleID: "pii-username", Category: "USERNAME", Priority: 90},
		{Start: 0, End: len(text), RuleID: "pii-email", Category: "EMAIL", Priority: 10},
	}
	got, err := Apply(text, spans)
	if err != nil {
		t.Fatal(err)
	}
	if got != "[REDACTED:EMAIL]" {
		t.Fatalf("Apply = %q", got)
	}
}

func TestApplyRejectsInvalidSpans(t *testing.T) {
	for _, spans := range [][]Span{
		{{Start: -1, End: 2}},
		{{Start: 2, End: 2}},
		{{Start: 4, End: 2}},
		{{Start: 0, End: 100}},
	} {
		if _, err := Apply("abc", spans); !errors.Is(err, ErrInvalidSpan) {
			t.Errorf("%+v: err = %v", spans, err)
		}
	}
}

func TestMarkerSanitizesCategory(t *testing.T) {
	if got := Marker("acme key"); got != "[REDACTED:ACME_KEY]" {
		t.Fatalf("Marker = %q", got)
	}
	if got := Marker(""); got != "[REDACTED:SECRET]" {
		t.Fatalf("empty Marker = %q", got)
	}
	if strings.Contains(Marker("EMAIL"), "alice") {
		t.Fatal("marker contains text")
	}
}

func TestApplyPreservingKeepsLength(t *testing.T) {
	text := `{"content":"mail alice@example.com password = \"hunter2\""}`
	emailStart := strings.Index(text, "alice@example.com")
	passwordStart := strings.Index(text, "hunter2")
	spans := []Span{
		{Start: emailStart, End: emailStart + len("alice@example.com"), RuleID: "pii-email", Category: "EMAIL", Priority: 70},
		{Start: passwordStart, End: passwordStart + len("hunter2"), RuleID: "password-assignment", Priority: 85},
	}
	got, err := ApplyPreserving(text, spans)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(text) {
		t.Fatalf("length changed: %d -> %d", len(text), len(got))
	}
	if strings.Contains(got, "alice@example.com") || strings.Contains(got, "hunter2") {
		t.Fatalf("secret survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:EMAIL] ") {
		t.Fatalf("email marker missing: %q", got)
	}
	if !strings.Contains(got, "*******") {
		t.Fatalf("short password not masked: %q", got)
	}
}
