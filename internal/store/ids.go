package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// stableID derives a UUID-shaped id from a kind and the parts that identify a
// record. Ids are deterministic so that every device, and every re-ingest,
// assigns the same id to the same record (ADR 0005). They never depend on
// content alone.
func stableID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	sum := h.Sum(nil)[:16]
	sum[6] = sum[6]&0x0f | 0x80 // UUID version 8 (custom)
	sum[8] = sum[8]&0x3f | 0x80 // RFC 4122 variant
	b := make([]byte, 36)
	hex.Encode(b[0:8], sum[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], sum[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], sum[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], sum[8:10])
	b[23] = '-'
	hex.Encode(b[24:], sum[10:])
	return string(b)
}

func conversationID(source, nativeID string) string {
	return stableID("conversation", source, nativeID)
}

// messageIDs assigns ids from the source's own message ids. A message without
// one is identified by its position; a repeated native id (which some sources
// emit) gets an occurrence suffix so ids stay unique.
func messageIDs(convID string, nativeIDs []*string) []string {
	ids := make([]string, len(nativeIDs))
	seen := make(map[string]int, len(nativeIDs))
	for i, native := range nativeIDs {
		if native == nil {
			ids[i] = stableID("message", convID, "#"+strconv.Itoa(i))
			continue
		}
		n := seen[*native]
		seen[*native] = n + 1
		if n == 0 {
			ids[i] = stableID("message", convID, *native)
		} else {
			ids[i] = stableID("message", convID, *native, strconv.Itoa(n))
		}
	}
	return ids
}

func blockID(messageID string, index int) string {
	return stableID("block", messageID, strconv.Itoa(index))
}

func rawEventID(convID string, line int) string {
	return stableID("raw_event", convID, strconv.Itoa(line))
}
