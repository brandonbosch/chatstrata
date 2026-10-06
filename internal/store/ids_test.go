package store

import (
	"regexp"
	"testing"

	"github.com/brandonbosch/chatstrata/internal/model"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestStableIDIsDeterministicAndUUIDShaped(t *testing.T) {
	a := conversationID("claude_code", "session-1")
	if a != conversationID("claude_code", "session-1") {
		t.Fatal("same input gave different ids")
	}
	if !uuidShape.MatchString(a) {
		t.Errorf("%s is not a version-8 UUID", a)
	}
	// Part boundaries matter: ("ab","c") and ("a","bc") must differ.
	if stableID("k", "ab", "c") == stableID("k", "a", "bc") {
		t.Error("ids collide across part boundaries")
	}
	if conversationID("claude_code", "x") == conversationID("codex_cli", "x") {
		t.Error("same native id in different sources collides")
	}
}

func TestMessageIDsHandleMissingAndRepeatedNativeIDs(t *testing.T) {
	ids := messageIDs("conv", []*string{model.Ptr("u1"), nil, model.Ptr("u1"), nil})
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id in %v", ids)
		}
		seen[id] = true
	}
	again := messageIDs("conv", []*string{model.Ptr("u1"), nil, model.Ptr("u1"), nil})
	for i := range ids {
		if ids[i] != again[i] {
			t.Errorf("message %d id not stable", i)
		}
	}
	// A message keeps its id when other messages are appended after it.
	longer := messageIDs("conv", []*string{model.Ptr("u1"), nil, model.Ptr("u1"), nil, model.Ptr("u2")})
	for i := range ids {
		if ids[i] != longer[i] {
			t.Errorf("message %d id changed after append", i)
		}
	}
}
