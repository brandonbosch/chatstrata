package cli

import (
	"bytes"
	"context"
	"flag"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseAllowsInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	db := fs.String("db", "", "")
	inc := fs.Bool("incremental", false, "")
	pos, err := parse(fs, []string{"claude_code", "--db", "x.duckdb", "--incremental"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"claude_code"}) || *db != "x.duckdb" || !*inc {
		t.Errorf("got pos=%v db=%q inc=%v", pos, *db, *inc)
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pos, err = parse(fs, []string{"--", "--not-a-flag", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"--not-a-flag", "x"}) {
		t.Errorf("after --: got %v", pos)
	}
}

func TestValidateReadOnly(t *testing.T) {
	allowed := []string{
		"SELECT 1",
		"  with x as (select 1) select * from x;",
		"-- comment\nSELECT 1",
		"DESCRIBE messages",
	}
	for _, q := range allowed {
		if err := validateReadOnly(q); err != nil {
			t.Errorf("%q rejected: %v", q, err)
		}
	}
	rejected := []string{
		"",
		"-- only a comment",
		"DELETE FROM messages",
		"/* sneaky */ drop table messages",
		"SELECT 1; DROP TABLE messages",
		"install fts",
	}
	for _, q := range rejected {
		if err := validateReadOnly(q); err == nil {
			t.Errorf("%q accepted", q)
		}
	}
}

func TestFormatValueMatchesPython(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{1.0, "1.0"},
		{0.5, "0.5"},
		{1e16, "1e+16"},
		{1.5e-5, "1.5e-05"},
		{123456789.0, "123456789.0"},
		{int64(3), "3"},
		{true, "True"},
		{"text", "text"},
		{time.Date(2026, 3, 14, 10, 0, 5, 0, time.UTC), "2026-03-14 10:00:05+00:00"},
		{time.Date(2026, 3, 14, 10, 0, 5, 120_000_000, time.UTC), "2026-03-14 10:00:05.120000+00:00"},
	}
	for _, c := range cases {
		if got := formatValue(c.in); got != c.want {
			t.Errorf("formatValue(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("a", 200) + "Needle" + strings.Repeat("b", 200)
	got := snippet(text, "needle other", 5)
	if got != "...aaaaaNeedlebbbbb..." {
		t.Errorf("snippet = %q", got)
	}
	if got := snippet("short text", "absent"); got != "short text" {
		t.Errorf("snippet without match = %q", got)
	}
}

func TestQueryOnMissingArchive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	db := filepath.Join(t.TempDir(), "missing.duckdb")
	code := Run(context.Background(), []string{"stats", "--db", db}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "No archive at") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestUnknownSourceIsExplained(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"ingest", "chatgpt", "--db", filepath.Join(t.TempDir(), "a.duckdb")}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Unknown source: chatgpt") {
		t.Errorf("code=%d stderr=%q", code, stderr.String())
	}
}
