package obslog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAndReadBack(t *testing.T) {
	root := t.TempDir()
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	obs := []*Observation{
		{Source: "claude_code", Locator: "s1", Kind: Append, Content: []byte("line one\n")},
		{Source: "claude_code", Locator: "s1", Kind: Append, Offset: 9, Content: []byte("line two\n")},
		{Source: "claude_code", Locator: "s2", Kind: Tombstone},
	}
	locs, err := l.Append(obs)
	if err != nil {
		t.Fatal(err)
	}
	if obs[0].Seq != 1 || obs[2].Seq != 3 {
		t.Fatalf("seqs = %d..%d", obs[0].Seq, obs[2].Seq)
	}
	got, err := l.ReadAt(locs[1])
	if err != nil {
		t.Fatal(err)
	}
	if got.Offset != 9 || !bytes.Equal(got.Content, []byte("line two\n")) || got.SHA256 == "" {
		t.Errorf("read back %+v", got)
	}

	// Reopening continues the sequence; the identity is kept.
	again, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if again.Identity != l.Identity {
		t.Error("identity changed on reopen")
	}
	more := []*Observation{{Source: "claude_code", Locator: "s3", Kind: Snapshot, Content: []byte("x")}}
	if _, err := again.Append(more); err != nil {
		t.Fatal(err)
	}
	if more[0].Seq != 4 {
		t.Errorf("seq after reopen = %d, want 4", more[0].Seq)
	}

	segs, err := again.Segments(l.Identity.Device)
	if err != nil || len(segs) != 2 {
		t.Fatalf("segments = %v, %v", segs, err)
	}
	var seen []uint64
	for _, s := range segs {
		if err := again.ReadSegment(s.Path, false, func(o *Observation, _ Location) error {
			seen = append(seen, o.Seq)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 4 || seen[3] != 4 {
		t.Errorf("read seqs %v", seen)
	}
}

func TestLargeBatchesSplitIntoSegments(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), MaxSegmentBytes/2+1)
	obs := []*Observation{
		{Source: "s", Locator: "a", Kind: Snapshot, Content: big},
		{Source: "s", Locator: "b", Kind: Snapshot, Content: big},
		{Source: "s", Locator: "c", Kind: Snapshot, Content: big},
	}
	if _, err := l.Append(obs); err != nil {
		t.Fatal(err)
	}
	segs, _ := l.Segments(l.Identity.Device)
	if len(segs) != 3 {
		t.Errorf("got %d segments, want 3", len(segs))
	}
}

func TestCorruptRecordIsReported(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locs, err := l.Append([]*Observation{{Source: "s", Locator: "a", Kind: Snapshot, Content: []byte("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Root, locs[0].Segment)
	b, _ := os.ReadFile(path)
	b[len(b)-1] ^= 0xff
	os.WriteFile(path, b, 0o600)
	if _, err := l.ReadAt(locs[0]); !errors.Is(err, ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt", err)
	}
}
