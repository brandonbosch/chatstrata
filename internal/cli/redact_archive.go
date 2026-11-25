package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/redactions"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

func runRedactScan(e *env, args []string) error {
	return runRedactArchiveScan(e, args, true)
}

func runRedactApply(e *env, args []string) error {
	return runRedactArchiveScan(e, args, false)
}

func runRedactArchiveScan(e *env, args []string, scanOnly bool) error {
	name := "redact apply"
	if scanOnly {
		name = "redact scan"
	}
	fs := newFlagSet(e, name, name+" [--db PATH] [--config PATH] [--profile fast|balanced|deep] [--dry-run]")
	db := fs.String("db", "", "Override the database path.")
	config := fs.String("config", "", "TOML redaction policy.")
	profile := fs.String("profile", "", "Override the policy profile: fast, balanced or deep.")
	dryRun := fs.Bool("dry-run", false, "Report matches without writing a redaction run.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	engine, err := loadRedactionEngine(*config, *profile)
	if err != nil {
		return err
	}
	if scanOnly || *dryRun {
		s, err := openForReading(e, *db)
		if s == nil || err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		records, stats, err := redactions.Scan(e.ctx, s, engine)
		if err != nil {
			return err
		}
		printScan(e, engine, records, stats)
		return nil
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()

	records, stats, err := redactions.Scan(e.ctx, a.store, engine)
	if err != nil {
		return err
	}
	printScan(e, engine, records, stats)
	if len(records) == 0 {
		return nil
	}

	dir := redactions.Dir(a.store.Path)
	path, err := redactions.WriteRun(dir, redactions.Header{
		CreatedAt:        time.Now().UTC(),
		RedactionVersion: engine.Version(),
	}, records)
	if err != nil {
		return err
	}
	applied, err := redactions.Apply(e.ctx, a.store, dir)
	if err != nil {
		return err
	}
	if applied.Applied > 0 && a.store.LoadFTS(e.ctx) {
		if err := a.store.RebuildFTS(e.ctx); err != nil {
			return fmt.Errorf("apply redactions, rebuild search index: %w", err)
		}
	}
	_, _ = fmt.Fprintf(e.stdout, "Run: %s\nApplied: %d targets, %d findings  Skipped: %d stale\n",
		path, applied.Applied, applied.Findings, applied.Skipped)
	_, _ = fmt.Fprintf(e.stdout, "Redaction version: %s\n", engine.Version())
	return nil
}

func printScan(e *env, engine *redact.Engine, records []redactions.Record, stats redactions.Stats) {
	_, _ = fmt.Fprintf(e.stdout, "Scanned: %d values  Matches: %d records, %d findings\n", stats.Rows, stats.Records, stats.Findings)
	for rule, n := range stats.Counts {
		_, _ = fmt.Fprintf(e.stdout, "  %-40s %d\n", rule, n)
	}
	_, _ = fmt.Fprintf(e.stdout, "Redaction version: %s\n", engine.Version())
	if len(records) == 0 {
		_, _ = fmt.Fprintln(e.stdout, "No redactions needed.")
	}
}

func runRedactPurge(e *env, args []string) error {
	fs := newFlagSet(e, "redact purge", "redact purge --hard --yes [--db PATH] [--config PATH] [--profile fast|balanced|deep] [--delete-old]")
	db := fs.String("db", "", "Override the database path.")
	config := fs.String("config", "", "TOML redaction policy.")
	profile := fs.String("profile", "", "Override the policy profile: fast, balanced or deep.")
	hard := fs.Bool("hard", false, "Required: rewrite local observation segments.")
	yes := fs.Bool("yes", false, "Required: confirm the destructive local rewrite.")
	deleteOld := fs.Bool("delete-old", false, "Remove the archived plaintext log after a successful swap.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !*hard {
		return usageError{"redact purge requires --hard (it rewrites local observation segments)"}
	}
	if !*yes {
		return usageError{"redact purge requires --yes"}
	}
	dbPath, err := store.ResolvePath(*db)
	if err != nil {
		return err
	}
	logPath := logDir(dbPath)
	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("observation log: %w", err)
	}
	spaceKey := filepath.Join(logPath, "space.key")
	if _, err := os.Stat(spaceKey); err == nil {
		return errors.New("this archive is paired for sync; a local purge cannot recall the relay ciphertext or other devices. Create a new space, re-collect with the policy, and re-pair, or remove space.key/sync.json if this archive is no longer synced")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check sync pairing: %w", err)
	}
	engine, err := loadRedactionEngine(*config, *profile)
	if err != nil {
		return err
	}
	stats, err := redactions.Purge(e.ctx, logPath, redactions.PurgeOptions{
		Engine: engine, DeleteOld: *deleteOld,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.stdout, "Rewrote: %d observations in %d segments  Redacted: %d\n",
		stats.Observations, stats.Segments, stats.Redacted)
	if stats.Redacted == 0 {
		_, _ = fmt.Fprintln(e.stdout, "No redactions needed; the log is unchanged.")
		return nil
	}
	if stats.OldLog != "" {
		if *deleteOld {
			_, _ = fmt.Fprintf(e.stdout, "Old plaintext log removed: %s\n", stats.OldLog)
		} else {
			_, _ = fmt.Fprintf(e.stdout, "Old log (still contains the original plaintext): %s\n", stats.OldLog)
		}
	}
	_, _ = fmt.Fprintln(e.stdout, "Source files, other devices, relay ciphertext and backups are not changed; rotate any credential found.")
	if err := runRebuild(e, []string{"--db", dbPath}); err != nil {
		return fmt.Errorf("log purged, but projection rebuild failed: %w", err)
	}
	// The raw source bytes and the parsed projection can differ (JSON escapes,
	// adapter transforms). A fresh scan over the rebuilt projection catches
	// anything the raw pass missed and records it in the overlay.
	applyArgs := []string{"--db", dbPath}
	if *config != "" {
		applyArgs = append(applyArgs, "--config", *config)
	}
	if *profile != "" {
		applyArgs = append(applyArgs, "--profile", *profile)
	}
	if err := runRedactApply(e, applyArgs); err != nil {
		return fmt.Errorf("projection rebuilt, but the verification scan failed: %w", err)
	}
	return nil
}

func loadRedactionEngine(config, profile string) (*redact.Engine, error) {
	policy, err := redact.LoadPolicy(config)
	if err != nil {
		return nil, err
	}
	if profile != "" {
		policy.Profile, err = redact.ParseProfile(profile)
		if err != nil {
			return nil, usageError{msg: err.Error()}
		}
	}
	return redact.NewEngine(policy)
}
