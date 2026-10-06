// Package project builds the DuckDB archive from the observation log.
//
// The projection is a pure function of the set of observations: the same
// observations produce the same archive whatever order they arrived in and
// on whichever device it is built (ADR 0005, spec/projection).
package project

import (
	"bytes"
	"sort"

	"github.com/brandonbosch/chatstrata/internal/obslog"
	"github.com/brandonbosch/chatstrata/internal/store"
)

// fragment is a byte range of a source file, from one observation.
type fragment struct {
	offset int64
	data   []byte
	obs    *store.IndexedObservation
}

func (f fragment) end() int64 { return f.offset + int64(len(f.data)) }

// candidate is one consistent version of a source file: every fragment that
// agrees with it, byte for byte, over the range they share.
type candidate struct {
	content      []byte
	contributors []*store.IndexedObservation
	legacy       bool
}

// reconstruct assembles the versions of a file that the fragments describe.
//
// Fragments that agree are combined, so overlapping appends from different
// devices, stale snapshots and duplicates all collapse into one version. A
// fragment that contradicts every version starts a new one: at offset 0 as a
// fresh file, otherwise as a fork sharing the bytes before it. A fragment
// beyond the end of every version waits for the gap to be filled. Versions
// that are a prefix of another are dropped.
func reconstruct(frags []fragment) []*candidate {
	sort.SliceStable(frags, func(i, j int) bool {
		a, b := frags[i], frags[j]
		if a.offset != b.offset {
			return a.offset < b.offset
		}
		if len(a.data) != len(b.data) {
			return len(a.data) > len(b.data)
		}
		if a.obs.Device != b.obs.Device {
			return a.obs.Device < b.obs.Device
		}
		return a.obs.Seq < b.obs.Seq
	})

	var cands []*candidate
	placed := make([]bool, len(frags))
	for progress := true; progress; {
		progress = false
		for i, f := range frags {
			if placed[i] {
				continue
			}
			fits := false
			for _, c := range cands {
				if agrees(c.content, f) {
					fits = true
					if f.end() > int64(len(c.content)) {
						c.content = append(c.content, f.data[int64(len(c.content))-f.offset:]...)
					}
				}
			}
			switch {
			case fits:
			case f.offset == 0:
				cands = append(cands, &candidate{content: append([]byte(nil), f.data...)})
			default:
				base := firstReaching(cands, f.offset)
				if base == nil {
					continue // a gap before this fragment; retry once it fills
				}
				content := append(append([]byte(nil), base.content[:f.offset]...), f.data...)
				cands = append(cands, &candidate{content: content})
			}
			placed[i] = true
			progress = true
		}
	}

	// Drop versions contained in a longer one.
	var kept []*candidate
	for i, c := range cands {
		contained := false
		for j, other := range cands {
			if i != j && len(other.content) >= len(c.content) && bytes.HasPrefix(other.content, c.content) &&
				(len(other.content) > len(c.content) || j < i) {
				contained = true
				break
			}
		}
		if !contained {
			kept = append(kept, c)
		}
	}
	// Attribute fragments to every version they agree with, so each version
	// knows which devices saw it.
	for _, c := range kept {
		for i, f := range frags {
			if placed[i] && f.end() <= int64(len(c.content)) && agrees(c.content, f) {
				c.contributors = append(c.contributors, f.obs)
			}
		}
	}
	return kept
}

// agrees reports whether a fragment starts within content and matches it over
// the bytes they share.
func agrees(content []byte, f fragment) bool {
	if f.offset > int64(len(content)) {
		return false
	}
	shared := min(int64(len(content)), f.end()) - f.offset
	return bytes.Equal(content[f.offset:f.offset+shared], f.data[:shared])
}

func firstReaching(cands []*candidate, offset int64) *candidate {
	for _, c := range cands {
		if int64(len(c.content)) >= offset {
			return c
		}
	}
	return nil
}

// devices lists the distinct devices that contributed to a candidate.
func (c *candidate) devices() []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range c.contributors {
		if !seen[o.Device] {
			seen[o.Device] = true
			out = append(out, o.Device)
		}
	}
	sort.Strings(out)
	return out
}

// representative picks the observation whose path and project hint describe
// the candidate: this device's latest if it has one, else the earliest by
// device and sequence number.
func (c *candidate) representative(self string) *store.IndexedObservation {
	var best *store.IndexedObservation
	for _, o := range c.contributors {
		switch {
		case best == nil:
			best = o
		case o.Device == self && (best.Device != self || o.Seq > best.Seq):
			best = o
		case best.Device != self && o.Device != self &&
			(o.Device < best.Device || (o.Device == best.Device && o.Seq < best.Seq)):
			best = o
		}
	}
	return best
}

// fragmentOf converts a non-tombstone observation into a fragment. Snapshots
// cover the file from offset 0.
func fragmentOf(o *store.IndexedObservation, content []byte) fragment {
	off := o.Offset
	if o.Kind == obslog.Snapshot {
		off = 0
	}
	return fragment{offset: off, data: content, obs: o}
}
