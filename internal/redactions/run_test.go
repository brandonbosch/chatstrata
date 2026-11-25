package redactions

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

func TestRunRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "archive.redactions")
	text := "mail alice@example.com"
	records := []Record{{
		TargetKind:  TargetBlockText,
		TargetID:    "block-1",
		InputSHA256: sha256Hex(text),
		Spans:       []redact.Span{{Start: 5, End: 22, RuleID: "pii-email", Category: "EMAIL", Priority: 70}},
	}}
	path, err := WriteRun(dir, Header{CreatedAt: time.Time{}, RedactionVersion: "rd2-test", RunID: "20260101T000000.000000000Z-test"}, records)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".zst" {
		t.Fatalf("path = %s", path)
	}
	runs, err := ReadRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Header.RunID != "20260101T000000.000000000Z-test" || len(runs[0].Records) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	got := runs[0].Records[0]
	if got.TargetKind != TargetBlockText || got.TargetID != "block-1" || got.InputSHA256 != sha256Hex(text) {
		t.Fatalf("record = %+v", got)
	}
	if !reflect.DeepEqual(got.Spans, records[0].Spans) {
		t.Fatalf("spans = %+v", got.Spans)
	}
	if _, err := ReadRuns(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}

func TestWriteRunRejectsInvalidRecords(t *testing.T) {
	dir := t.TempDir()
	for _, record := range []Record{
		{TargetKind: "bogus", TargetID: "x", InputSHA256: sha256Hex("x"), Spans: []redact.Span{{Start: 0, End: 1}}},
		{TargetKind: TargetBlockText, TargetID: "", InputSHA256: sha256Hex("x"), Spans: []redact.Span{{Start: 0, End: 1}}},
		{TargetKind: TargetBlockText, TargetID: "x", InputSHA256: "no", Spans: []redact.Span{{Start: 0, End: 1}}},
		{TargetKind: TargetBlockText, TargetID: "x", InputSHA256: sha256Hex("x")},
	} {
		if _, err := WriteRun(dir, Header{RedactionVersion: "rd2-test"}, []Record{record}); err == nil {
			t.Errorf("accepted invalid record %+v", record)
		}
	}
}
