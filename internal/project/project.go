package project

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/obslog"
	"github.com/brandonbosch/chatstrata/internal/store"
)

// Projector keeps the archive in step with the observation log.
type Projector struct {
	Store   *store.Store
	Log     *obslog.Log
	Sources map[string]model.Source

	registered map[string]bool
}

// Outcome says what projecting one conversation did.
type Outcome int

const (
	Unchanged Outcome = iota
	Stored            // inserted or replaced
	Removed           // hidden by a tombstone
	Pending           // nothing projectable yet (gaps, or no messages)
	Unported          // no Go adapter for the source yet; kept in the log only
)

// Project rebuilds one conversation from all of its observations.
func (p *Projector) Project(ctx context.Context, key obslog.Key) (Outcome, error) {
	all, err := p.Store.ObservationsFor(ctx, key)
	if err != nil {
		return Unchanged, err
	}
	if len(all) == 0 {
		return Pending, nil
	}
	for _, o := range all {
		if o.Kind == obslog.Tombstone {
			removed, err := p.Store.RemoveConversation(ctx, key.Source, key.Locator)
			if err != nil || !removed {
				return Unchanged, err
			}
			return Removed, nil
		}
	}
	src, ok := p.Sources[key.Source]
	if !ok {
		return Unported, nil
	}

	var live []fragment
	var cands []*candidate
	seenLegacy := map[string]bool{}
	for i := range all {
		o := &all[i]
		rec, err := p.Log.ReadAt(o.Location)
		if err != nil {
			return Unchanged, err
		}
		if !o.Legacy {
			live = append(live, fragmentOf(o, rec.Content))
			continue
		}
		// Legacy content is re-serialized JSON, so its bytes never line up
		// with the original file. Each import is its own version, merged at
		// the message level below and never reported as a divergence.
		if seenLegacy[o.SHA256] {
			continue
		}
		seenLegacy[o.SHA256] = true
		cands = append(cands, &candidate{content: rec.Content, contributors: []*store.IndexedObservation{o}, legacy: true})
	}
	cands = append(reconstruct(live), cands...)
	if len(cands) == 0 {
		return Pending, nil
	}

	parsed := make([]*model.Conversation, len(cands))
	for i, c := range cands {
		rep := c.representative(p.Log.Identity.Device)
		conv, err := src.Parse(model.Handle{SourceNativeID: key.Locator, Path: rep.Path, Project: rep.ProjectHint}, c.content)
		if err != nil {
			return Unchanged, fmt.Errorf("parse %s: %w", key.Locator, err)
		}
		parsed[i] = conv
	}
	order(cands, parsed)
	conv, divs := merge(cands, parsed)
	if len(conv.Messages) == 0 {
		return Pending, nil
	}

	if err := p.ensureSource(ctx, src); err != nil {
		return Unchanged, err
	}
	action, err := p.Store.Ingest(ctx, key.Source, conv, nil)
	if err != nil {
		return Unchanged, err
	}
	if err := p.Store.ReplaceDivergences(ctx, key.Source, key.Locator, divs); err != nil {
		return Unchanged, err
	}
	if action == store.Unchanged {
		return Unchanged, nil
	}
	return Stored, nil
}

func (p *Projector) ensureSource(ctx context.Context, src model.Source) error {
	if p.registered == nil {
		p.registered = map[string]bool{}
	}
	if p.registered[src.Name()] {
		return nil
	}
	if err := p.Store.EnsureSource(ctx, src); err != nil {
		return err
	}
	p.registered[src.Name()] = true
	return nil
}

// order sorts versions so the primary comes first: live before legacy, then
// most messages, then longest content, then the earliest contributor.
func order(cands []*candidate, parsed []*model.Conversation) {
	idx := make([]int, len(cands))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ca, cb := cands[idx[a]], cands[idx[b]]
		if ca.legacy != cb.legacy {
			return !ca.legacy
		}
		if na, nb := len(parsed[idx[a]].Messages), len(parsed[idx[b]].Messages); na != nb {
			return na > nb
		}
		if len(ca.content) != len(cb.content) {
			return len(ca.content) > len(cb.content)
		}
		fa, fb := ca.contributors[0], cb.contributors[0]
		if fa.Device != fb.Device {
			return fa.Device < fb.Device
		}
		return fa.Seq < fb.Seq
	})
	c2 := make([]*candidate, len(cands))
	p2 := make([]*model.Conversation, len(cands))
	for i, j := range idx {
		c2[i], p2[i] = cands[j], parsed[j]
	}
	copy(cands, c2)
	copy(parsed, p2)
}

// messageKey identifies a message across versions: its source-native id, or
// its position when the source has none.
func messageKey(m model.Message, index int) string {
	if m.SourceNativeID != nil {
		return "id:" + *m.SourceNativeID
	}
	return fmt.Sprintf("pos:%d", index)
}

type block struct {
	messages  []model.Message
	anchor    int // index in the primary of the message this block follows, or -1
	candidate int
}

// merge combines the versions of a conversation. The primary's messages come
// first; messages only other versions have are kept as blocks after the
// message they follow. Everything after the earliest fork point is grouped
// into branches ordered by their first message's time, then id.
func merge(cands []*candidate, parsed []*model.Conversation) (*model.Conversation, []store.Divergence) {
	primary := parsed[0]
	if len(parsed) == 1 {
		return primary, nil
	}

	position := map[string]int{}
	for i, m := range primary.Messages {
		position[messageKey(m, i)] = i
	}
	present := map[string]bool{}
	for k := range position {
		present[k] = true
	}

	var blocks []block
	for ci := 1; ci < len(parsed); ci++ {
		var current *block
		anchor := -1
		for i, m := range parsed[ci].Messages {
			k := messageKey(m, i)
			if present[k] {
				if pos, ok := position[k]; ok {
					anchor = pos
				}
				current = nil
				continue
			}
			present[k] = true
			if current == nil {
				blocks = append(blocks, block{anchor: anchor, candidate: ci})
				current = &blocks[len(blocks)-1]
			}
			current.messages = append(current.messages, m)
		}
	}
	if len(blocks) == 0 {
		return withRawEvents(primary, parsed), nil
	}

	fork := len(primary.Messages) - 1
	for _, b := range blocks {
		fork = min(fork, b.anchor)
	}
	groups := []block{}
	if tail := primary.Messages[fork+1:]; len(tail) > 0 {
		groups = append(groups, block{messages: tail, anchor: fork, candidate: 0})
	}
	groups = append(groups, blocks...)
	sort.SliceStable(groups, func(i, j int) bool {
		return firstBefore(groups[i].messages[0], groups[j].messages[0])
	})

	merged := *primary
	merged.Messages = append([]model.Message(nil), primary.Messages[:fork+1]...)
	for _, g := range groups {
		merged.Messages = append(merged.Messages, g.messages...)
	}
	merged.StartedAt, merged.EndedAt = nil, nil
	for _, m := range merged.Messages {
		if m.CreatedAt == nil {
			continue
		}
		if merged.StartedAt == nil || m.CreatedAt.Before(*merged.StartedAt) {
			merged.StartedAt = m.CreatedAt
		}
		if merged.EndedAt == nil || m.CreatedAt.After(*merged.EndedAt) {
			merged.EndedAt = m.CreatedAt
		}
	}

	// A divergence is two live versions disagreeing. Legacy imports only fill
	// in history and are not reported.
	var divs []store.Divergence
	liveBranch := false
	for _, b := range blocks {
		if !cands[b.candidate].legacy {
			liveBranch = true
		}
	}
	if liveBranch {
		var forkAfter *string
		if fork >= 0 {
			forkAfter = primary.Messages[fork].SourceNativeID
		}
		for _, g := range groups {
			if cands[g.candidate].legacy || g.messages[0].SourceNativeID == nil {
				continue
			}
			divs = append(divs, store.Divergence{
				ForkAfter:     forkAfter,
				BranchMessage: *g.messages[0].SourceNativeID,
				Devices:       cands[g.candidate].devices(),
			})
		}
	}
	return withRawEvents(&merged, parsed), divs
}

func firstBefore(a, b model.Message) bool {
	switch {
	case a.CreatedAt != nil && b.CreatedAt != nil && !a.CreatedAt.Equal(*b.CreatedAt):
		return a.CreatedAt.Before(*b.CreatedAt)
	case (a.CreatedAt == nil) != (b.CreatedAt == nil):
		return a.CreatedAt != nil
	}
	return deref(a.SourceNativeID) < deref(b.SourceNativeID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// withRawEvents returns conv with the raw events of every version: the
// primary's, then any from other versions that it lacks, compared as JSON
// values so re-serialized legacy events match their originals.
func withRawEvents(conv *model.Conversation, parsed []*model.Conversation) *model.Conversation {
	out := *conv
	seen := map[string]bool{}
	out.RawEvents = nil
	for _, p := range parsed {
		for _, raw := range p.RawEvents {
			k := canonicalJSON(raw)
			if seen[k] {
				continue
			}
			seen[k] = true
			out.RawEvents = append(out.RawEvents, raw)
		}
	}
	if len(parsed) > 1 {
		md := map[string]any{}
		for k, v := range conv.Metadata {
			md[k] = v
		}
		md["event_count"] = len(out.RawEvents)
		out.Metadata = md
	}
	return &out
}

func canonicalJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.Marshal(v) // map keys are sorted
	if err != nil {
		return string(raw)
	}
	return string(b)
}
