package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/brandonbosch/chatstrata/internal/devsync"
	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/relay"
	"github.com/brandonbosch/chatstrata/internal/store"
)

func runRelay(e *env, args []string) error {
	fs := newFlagSet(e, "relay", "relay [--listen ADDR] [--data DIR]")
	listen := fs.String("listen", "127.0.0.1:8787", "Address to listen on. Keep it on localhost and put HTTPS in front (e.g. `tailscale serve`).")
	data := fs.String("data", "", "Where to keep segments (default: <data dir>/relay).")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dir := *data
	if dir == "" {
		db, err := store.DefaultPath()
		if err != nil {
			return err
		}
		dir = filepath.Join(filepath.Dir(db), "relay")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           (&relay.Server{Dir: dir, Log: log.New(e.stdout, "relay ", log.LstdFlags)}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-e.ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(e.stdout, "chatstrata relay listening on http://%s, storing in %s\n", *listen, dir)
	fmt.Fprintln(e.stdout, "Expose it on your tailnet with HTTPS, e.g.: tailscale serve --bg http://"+*listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runPair(e *env, args []string) error {
	fs := newFlagSet(e, "pair", "pair [--relay URL] [--db PATH]")
	relayURL := fs.String("relay", "", "The relay's URL (required the first time).")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer a.Close()

	space, err := devsync.Load(a.log.Root, a.log.Identity.Space)
	switch {
	case err == nil:
		if *relayURL != "" {
			if space.Relay, err = devsync.ValidateRelay(*relayURL); err != nil {
				return usageError{err.Error()}
			}
		}
	case errors.Is(err, devsync.ErrNotPaired):
		if *relayURL == "" {
			return usageError{"pass --relay URL the first time, e.g. --relay https://relay.your-tailnet.ts.net"}
		}
		r, err := devsync.ValidateRelay(*relayURL)
		if err != nil {
			return usageError{err.Error()}
		}
		key, err := devsync.NewKey()
		if err != nil {
			return err
		}
		space = &devsync.Space{ID: a.log.Identity.Space, Key: key, Relay: r}
	default:
		return err
	}
	if err := devsync.Save(a.log.Root, space); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "This device syncs through %s.\n\n", space.Relay)
	fmt.Fprintln(e.stdout, "To add another device, run this there. The code contains the space key:")
	fmt.Fprintln(e.stdout, "anyone who has it can read this archive, so share it only with yourself.")
	fmt.Fprintln(e.stdout)
	fmt.Fprintf(e.stdout, "  chatstrata join %s\n", space.PairingCode())
	return nil
}

func runJoin(e *env, args []string) error {
	fs := newFlagSet(e, "join", "join CODE [--db PATH]")
	db := fs.String("db", "", "Override the database path.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"expected the code printed by `chatstrata pair`"}
	}
	space, err := devsync.ParsePairingCode(positional[0])
	if err != nil {
		return usageError{err.Error()}
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer a.Close()

	if existing, err := devsync.Load(a.log.Root, a.log.Identity.Space); err == nil && existing.ID != space.ID {
		return errors.New("this device already belongs to another space; it can only be in one")
	}
	if err := a.log.AdoptSpace(space.ID); err != nil {
		return err
	}
	if err := devsync.Save(a.log.Root, space); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Joined. This device (%s) now syncs through %s.\n", short(a.log.Identity.Device), space.Relay)
	return syncNow(e, a, space)
}

func runSync(e *env, args []string) error {
	fs := newFlagSet(e, "sync", "sync [--db PATH]")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer a.Close()
	space, err := devsync.Load(a.log.Root, a.log.Identity.Space)
	if err != nil {
		return err
	}
	return syncNow(e, a, space)
}

// syncNow exchanges segments with the relay and applies what arrived.
func syncNow(e *env, a *archive, space *devsync.Space) error {
	res, err := devsync.NewClient(space).Sync(e.ctx, a.log)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	stats, err := a.proj.CatchUp(e.ctx, false, e.stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Synced. Uploaded: %d segments  Downloaded: %d segments  Conversations updated: %d\n",
		res.Uploaded, res.Downloaded, stats.Stored+stats.Removed)
	return nil
}

func runDevices(e *env, args []string) error {
	fs := newFlagSet(e, "devices", "devices [--db PATH]")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer a.Close()

	self := a.log.Identity.Device
	fmt.Fprintf(e.stdout, "This device: %s\nSpace:       %s\n", self, a.log.Identity.Space)
	space, err := devsync.Load(a.log.Root, a.log.Identity.Space)
	if err == nil {
		fmt.Fprintf(e.stdout, "Relay:       %s\n", space.Relay)
	} else {
		fmt.Fprintln(e.stdout, "Relay:       not set up (see `chatstrata pair`)")
	}

	local, err := a.log.Devices()
	if err != nil {
		return err
	}
	rows := map[string][2]string{self: {"0", "-"}}
	for _, d := range local {
		segs, err := a.log.Segments(d)
		if err != nil {
			return err
		}
		rows[d] = [2]string{fmt.Sprint(len(segs)), "-"}
	}
	if space != nil {
		remote, err := devsync.NewClient(space).Devices(e.ctx)
		if err != nil {
			fmt.Fprintf(e.stdout, "\n(relay not reachable: %s)\n", err)
		}
		for _, d := range remote {
			r := rows[d.Device]
			if r[0] == "" {
				r[0] = "0"
			}
			r[1] = fmt.Sprint(d.Segments)
			rows[d.Device] = r
		}
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Fprintf(e.stdout, "\n  %-34s %10s %10s\n", "device", "local", "on relay")
	for _, id := range ids {
		mark := ""
		if id == self {
			mark = "  (this device)"
		}
		fmt.Fprintf(e.stdout, "  %-34s %10s %10s%s\n", id, rows[id][0], rows[id][1], mark)
	}
	return nil
}

func runDaemon(e *env, args []string) error {
	fs := newFlagSet(e, "daemon", "daemon [--interval 5m] [--db PATH]")
	interval := fs.Duration("interval", 5*time.Minute, "Time between runs.")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *interval < 10*time.Second {
		return usageError{"--interval must be at least 10s"}
	}
	logger := log.New(e.stdout, "", log.LstdFlags)
	logger.Printf("chatstrata daemon: collecting and syncing every %s", *interval)
	for {
		// The archive is opened per run, so other commands can read it in
		// between.
		if err := daemonRun(e, *db, logger); err != nil {
			logger.Printf("run failed: %s", err)
		}
		select {
		case <-e.ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func daemonRun(e *env, db string, logger *log.Logger) error {
	a, err := openArchive(e, db)
	if err != nil {
		return err
	}
	defer a.Close()
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		src := sources[name]
		handles, err := src.Discover("")
		if errors.Is(err, model.ErrNotFound) {
			continue // not installed on this machine
		}
		if err != nil {
			logger.Printf("%s: %s", name, err)
			continue
		}
		if err := a.store.EnsureSource(e.ctx, src); err != nil {
			return err
		}
		collected, err := a.proj.Collect(e.ctx, src, handles, true, e.stderr)
		if err != nil {
			return err
		}
		if collected.Logged > 0 {
			logger.Printf("%s: logged %d changed conversations", name, collected.Logged)
		}
	}
	stats, err := a.proj.CatchUp(e.ctx, false, e.stderr)
	if err != nil {
		return err
	}
	if stats.Stored > 0 {
		logger.Printf("archive: %d conversations updated", stats.Stored)
	}

	space, err := devsync.Load(a.log.Root, a.log.Identity.Space)
	if errors.Is(err, devsync.ErrNotPaired) {
		return nil
	}
	if err != nil {
		return err
	}
	res, err := devsync.NewClient(space).Sync(e.ctx, a.log)
	if err != nil {
		// The relay being away is normal: keep collecting, sync next time.
		logger.Printf("sync skipped: %s", err)
		return nil
	}
	stats, err = a.proj.CatchUp(e.ctx, false, e.stderr)
	if err != nil {
		return err
	}
	if res.Uploaded+res.Downloaded > 0 {
		logger.Printf("sync: uploaded %d, downloaded %d segments, %d conversations updated",
			res.Uploaded, res.Downloaded, stats.Stored+stats.Removed)
	}
	return nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
