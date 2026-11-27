package project

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/brandonbosch/chatstrata/v2/internal/label"
	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redactions"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// Stats counts what a catch-up did.
type Stats struct {
	Segments int
	Stored   int
	Removed  int
	Unported int
	Failed   int
	// Redacted counts projection rows changed by the archive overlay.
	Redacted int
	// Labels counts classifier label rows replayed from chatstrata-go.labels/.
	Labels int
	// Reindexed is set when the full-text search index was rebuilt.
	Reindexed bool
}

// CatchUp indexes every segment the archive hasn't seen yet, from any device,
// and re-projects the conversations they touch. It is how the archive
// recovers from a crash between writing the log and updating the
// projection, and how segments from other devices are applied.
//
// With ownState set it also recomputes this device's collector state from the
// log, which a freshly rebuilt archive needs so the collector doesn't log
// every file again.
func (p *Projector) CatchUp(ctx context.Context, ownState bool, errs io.Writer) (Stats, error) {
	var stats Stats
	devices, err := p.Log.Devices()
	if err != nil {
		return stats, err
	}
	var affected []obslog.Key
	seen := map[obslog.Key]bool{}
	for _, device := range devices {
		segs, err := p.Log.Segments(device)
		if err != nil {
			return stats, err
		}
		for _, seg := range segs {
			done, err := p.Store.IsSegmentIndexed(ctx, seg.Path)
			if err != nil {
				return stats, err
			}
			if done {
				continue
			}
			var rows []store.IndexedObservation
			err = p.Log.ReadSegment(seg.Path, false, func(o *obslog.Observation, loc obslog.Location) error {
				if o.Device != device {
					return fmt.Errorf("%s holds an observation from device %s", seg.Path, o.Device)
				}
				if o.Space != p.Log.Identity.Space {
					return fmt.Errorf("%s belongs to a different space", seg.Path)
				}
				rows = append(rows, store.IndexedObservation{Observation: *o, Location: loc})
				return nil
			})
			if err != nil {
				return stats, err
			}
			keys, err := p.Store.IndexSegment(ctx, seg.Path, rows)
			if err != nil {
				return stats, err
			}
			stats.Segments++
			for _, k := range keys {
				if !seen[k] {
					seen[k] = true
					affected = append(affected, k)
				}
			}
		}
	}

	for _, key := range affected {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if ownState {
			if err := p.recomputeOwnState(ctx, key); err != nil {
				return stats, err
			}
		}
		outcome, err := p.Project(ctx, key)
		if err != nil {
			stats.Failed++
			_, _ = fmt.Fprintf(errs, "  ! failed to project %s %s: %s\n", key.Source, key.Locator, err)
			continue
		}
		switch outcome {
		case Stored:
			stats.Stored++
		case Removed:
			stats.Removed++
		case Unported:
			stats.Unported++
		}
	}

	// Replay the archive redaction overlay after projection, so rows rewritten
	// by a re-ingest or a rebuild do not briefly expose their original text.
	// Only the conversations projected above are read: replaying every record
	// would read the whole archive on each cycle.
	if p.Store != nil {
		targets, err := p.redactionTargets(ctx, affected)
		if err != nil {
			return stats, fmt.Errorf("select archive redaction targets: %w", err)
		}
		applied, err := redactions.ApplyTargets(ctx, p.Store, p.redactionsDir(), targets)
		if err != nil {
			return stats, fmt.Errorf("apply archive redactions: %w", err)
		}
		stats.Redacted = applied.Applied

		replayed, err := label.Replay(ctx, p.Store, p.labelsDir(), ownState)
		if err != nil {
			return stats, fmt.Errorf("replay classifier labels: %w", err)
		}
		stats.Labels = replayed.Applied
	}

	// Keep the search index in step. DuckDB rebuilds it whole, so it's
	// rebuilt only when enough content is new (store.FTSNeedsRebuild);
	// until then `search` finds the newest content by substring matching.
	// Redaction changes existing text, so force a rebuild when it applied.
	// Skipped when the FTS extension isn't installed, as search then
	// substring-matches everything.
	if (stats.Stored+stats.Removed+stats.Redacted > 0) && p.Store.LoadFTS(ctx) {
		if stats.Redacted > 0 {
			if err := p.Store.RebuildFTS(ctx); err != nil {
				return stats, fmt.Errorf("update search index: %w", err)
			}
			stats.Reindexed = true
		} else {
			exists, indexed, pending, err := p.Store.FTSCoverage(ctx)
			if err != nil {
				return stats, fmt.Errorf("check search index: %w", err)
			}
			if store.FTSNeedsRebuild(exists, indexed, pending) {
				if err := p.Store.RebuildFTS(ctx); err != nil {
					return stats, fmt.Errorf("update search index: %w", err)
				}
				stats.Reindexed = true
			}
		}
	}
	return stats, nil
}

// redactionTargets lists the projection cells that belong to the conversations
// in keys. ApplyTargets reads only these, so an overlay with hundreds of
// thousands of records costs one conversation's rows per catch-up cycle.
func (p *Projector) redactionTargets(ctx context.Context, keys []obslog.Key) (map[redactions.TargetRef]struct{}, error) {
	out := make(map[redactions.TargetRef]struct{})
	for _, key := range keys {
		var convID string
		err := p.Store.Conn().QueryRowContext(ctx,
			`SELECT id FROM conversations WHERE source_id = ? AND source_native_id = ?`,
			key.Source, key.Locator).Scan(&convID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[redactions.TargetRef{Kind: redactions.TargetConversationTitle, ID: convID}] = struct{}{}

		if err := addBlockTargets(ctx, p.Store.Conn(), convID, out); err != nil {
			return nil, err
		}
		if err := addEventTargets(ctx, p.Store.Conn(), key.Source, key.Locator, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func addBlockTargets(ctx context.Context, conn *sql.Conn, convID string, out map[redactions.TargetRef]struct{}) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT cb.id
		FROM content_blocks cb
		JOIN messages m ON m.id = cb.message_id
		WHERE m.conversation_id = ?`, convID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		out[redactions.TargetRef{Kind: redactions.TargetBlockText, ID: id}] = struct{}{}
		out[redactions.TargetRef{Kind: redactions.TargetBlockPayload, ID: id}] = struct{}{}
	}
	return rows.Err()
}

func addEventTargets(ctx context.Context, conn *sql.Conn, source, locator string, out map[redactions.TargetRef]struct{}) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT id FROM raw_events WHERE source_id = ? AND source_native_conversation_id = ?`,
		source, locator)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		out[redactions.TargetRef{Kind: redactions.TargetRawEventPayload, ID: id}] = struct{}{}
	}
	return rows.Err()
}

func (p *Projector) redactionsDir() string {
	if p.RedactionsDir != "" {
		return p.RedactionsDir
	}
	if p.Store == nil {
		return ""
	}
	return redactions.Dir(p.Store.Path)
}

func (p *Projector) labelsDir() string {
	if p.LabelsDir != "" {
		return p.LabelsDir
	}
	if p.Store == nil {
		return ""
	}
	return label.Dir(p.Store.Path)
}

// recomputeOwnState replays this device's own observations of a conversation
// to find what it last logged.
func (p *Projector) recomputeOwnState(ctx context.Context, key obslog.Key) error {
	all, err := p.Store.ObservationsFor(ctx, key)
	if err != nil {
		return err
	}
	var content []byte
	own := false
	for _, o := range all {
		if o.Device != p.Log.Identity.Device || o.Kind == obslog.Tombstone || o.Legacy {
			continue
		}
		rec, err := p.Log.ReadAt(o.Location)
		if err != nil {
			return err
		}
		own = true
		switch {
		case o.Kind == obslog.Snapshot:
			content = rec.Content
		case o.Offset <= int64(len(content)):
			content = append(content[:o.Offset:o.Offset], rec.Content...)
		}
	}
	if !own {
		return nil
	}
	sum := sha256.Sum256(content)
	return p.Store.SetCollectorState(ctx, key, store.CollectorState{
		Length: int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]),
	})
}
