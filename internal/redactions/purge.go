package redactions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

// PurgeStats counts a log rewrite.
type PurgeStats struct {
	Segments     int
	Observations int
	Redacted     int
	OldLog       string
}

// PurgeOptions controls the destructive local rewrite.
type PurgeOptions struct {
	Engine    *redact.Engine
	DeleteOld bool
}

// Purge redacts this device's observation segments into a new log and swaps it
// into place, leaving the old directory as <log>.purged-<timestamp>. It
// refuses a log that already holds another device's segments: that archive
// needs a new space and re-collection, not a local rewrite. It does not touch
// the DuckDB projection; the caller rebuilds that from the new log.
func Purge(ctx context.Context, logPath string, opts PurgeOptions) (PurgeStats, error) {
	var stats PurgeStats
	if opts.Engine == nil {
		return stats, errors.New("purge requires a redaction engine")
	}
	log, err := obslog.Open(logPath)
	if err != nil {
		return stats, err
	}
	devices, err := log.Devices()
	if err != nil {
		return stats, err
	}
	var active []string
	for _, device := range devices {
		segs, err := log.Segments(device)
		if err != nil {
			return stats, err
		}
		if len(segs) > 0 {
			active = append(active, device)
		}
	}
	if len(active) > 1 {
		return stats, errors.New("this log holds segments from more than one device; a local purge cannot recall them — create a new space, re-collect with the policy, and re-pair devices")
	}
	if len(active) == 0 {
		return stats, errors.New("the observation log is empty")
	}
	device := active[0]
	if device != log.Identity.Device {
		return stats, fmt.Errorf("the observation log belongs to device %s, not this device; recover it before purging", device)
	}

	staging := uniquePath(logPath + ".purge-" + time.Now().UTC().Format("20060102T150405"))
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return stats, err
	}
	defer func() {
		if stats.OldLog == "" {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := copyRootFiles(logPath, staging); err != nil {
		return stats, err
	}
	stage, err := obslog.Open(staging)
	if err != nil {
		return stats, err
	}

	segs, err := log.Segments(device)
	if err != nil {
		return stats, err
	}
	for _, seg := range segs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		var obs []*obslog.Observation
		if err := log.ReadSegment(seg.Path, true, func(o *obslog.Observation, _ obslog.Location) error {
			stats.Observations++
			scope := redact.Scope{Project: o.ProjectHint, Path: o.Path}
			changed := false
			if o.Kind != obslog.Tombstone && len(o.Content) > 0 {
				redacted, contentChanged, err := redactPreserving(opts.Engine, scope, string(o.Content))
				if err != nil {
					return fmt.Errorf("redact %s %s: %w", o.Source, o.Locator, err)
				}
				if contentChanged {
					o.Content = []byte(redacted)
					changed = true
				}
			}
			path, pathChanged, err := redactPreserving(opts.Engine, scope, o.Path)
			if err != nil {
				return err
			}
			if pathChanged {
				o.Path = path
				changed = true
			}
			hint, hintChanged, err := redactPreserving(opts.Engine, scope, o.ProjectHint)
			if err != nil {
				return err
			}
			if hintChanged {
				o.ProjectHint = hint
				changed = true
			}
			if changed {
				o.Redacted = true
				stats.Redacted++
			}
			obs = append(obs, o)
			return nil
		}); err != nil {
			return stats, err
		}
		if err := stage.WriteSegment(device, obs); err != nil {
			return stats, fmt.Errorf("write purged segment %s: %w", seg.Path, err)
		}
		stats.Segments++
	}
	if stats.Redacted == 0 {
		return stats, nil
	}

	old := uniquePath(logPath + ".purged-" + time.Now().UTC().Format("20060102T150405"))
	if err := os.Rename(logPath, old); err != nil {
		return stats, fmt.Errorf("archive old log: %w", err)
	}
	if err := os.Rename(staging, logPath); err != nil {
		_ = os.Rename(old, logPath)
		return stats, fmt.Errorf("install purged log: %w", err)
	}
	stats.OldLog = old
	if opts.DeleteOld {
		if err := os.RemoveAll(old); err != nil {
			return stats, fmt.Errorf("remove old log %s: %w", old, err)
		}
	}
	return stats, nil
}

// copyRootFiles preserves identity.json, space.key, sync.json and any future
// log-root metadata. Segment directories are re-encoded separately.
func copyRootFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if info, err := entry.Info(); err == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, mode); err != nil {
			return err
		}
	}
	return nil
}

func redactPreserving(engine *redact.Engine, scope redact.Scope, text string) (string, bool, error) {
	if text == "" {
		return text, false, nil
	}
	result, err := engine.RedactScope(scope, text)
	if err != nil || len(result.Findings) == 0 {
		return text, false, err
	}
	spans := make([]redact.Span, len(result.Findings))
	for i, finding := range result.Findings {
		spans[i] = finding.Span()
	}
	redacted, err := redact.ApplyPreserving(text, spans)
	if err != nil {
		return "", false, err
	}
	return redacted, redacted != text, nil
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", path, i)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}
