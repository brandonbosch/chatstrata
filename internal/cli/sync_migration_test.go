package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/devsync"
	"github.com/brandonbosch/chatstrata/v2/internal/relay"
)

func TestRelayMigrationRestoresRetiredDevice(t *testing.T) {
	old := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer old.Close()
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	b := newDevice(t, map[string][]byte{noCwd: transcript(t, noCwd, 0)})
	for _, d := range []device{a, b} {
		run(t, "ingest", "claude_code", "--path", d.root, "--db", d.db)
	}
	code := pairingCode(t, run(t, "pair", "--relay", old.URL, "--db", a.db))
	run(t, "sync", "--db", a.db)
	run(t, "join", code, "--db", b.db)
	run(t, "sync", "--db", a.db)
	want := withoutPaths(dump(t, a.db))
	old.Close() // B never connects again, and the old relay is gone.

	root := strings.TrimSuffix(a.db, ".duckdb") + ".log"
	keyBefore, err := os.ReadFile(filepath.Join(root, "space.key"))
	if err != nil {
		t.Fatal(err)
	}
	identityBefore, err := os.ReadFile(filepath.Join(root, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var failPuts atomic.Bool
	relayDir := t.TempDir()
	handler := (&relay.Server{Dir: relayDir}).Handler()
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && failPuts.Load() {
			http.Error(w, "simulated outage", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer replacement.Close()

	out := run(t, "sync", "--set-relay", replacement.URL+"/", "--db", a.db)
	if !strings.Contains(out, "This device now syncs through "+replacement.URL+".") ||
		strings.Contains(out, "chatstrata-join:") || strings.Contains(out, strings.TrimSpace(string(keyBefore))) {
		t.Fatalf("relay change output is incorrect or exposes the key: %q", out)
	}
	keyAfter, _ := os.ReadFile(filepath.Join(root, "space.key"))
	identityAfter, _ := os.ReadFile(filepath.Join(root, "identity.json"))
	if !bytes.Equal(keyBefore, keyAfter) || !bytes.Equal(identityBefore, identityAfter) {
		t.Fatal("moving the relay changed the key or device/space identity")
	}
	space, err := devsync.ParsePairingCode(code)
	if err != nil {
		t.Fatal(err)
	}
	space.Relay = replacement.URL
	// Ordinary sync did not restore B's history. Reseed must do so.
	c := newDevice(t, nil)
	run(t, "join", space.PairingCode(), "--db", c.db)
	if reflect.DeepEqual(withoutPaths(dump(t, c.db)), want) {
		t.Fatal("ordinary sync unexpectedly uploaded another device's segments")
	}
	failPuts.Store(true)
	if _, err := runErr("sync", "--reseed", "--db", a.db); err == nil || !strings.Contains(err.Error(), "simulated outage") {
		t.Fatalf("reseed outage: %v", err)
	}
	failPuts.Store(false)
	run(t, "sync", "--set-relay", replacement.URL, "--reseed", "--db", a.db)
	d := newDevice(t, nil)
	run(t, "join", space.PairingCode(), "--db", d.db)
	if !reflect.DeepEqual(withoutPaths(dump(t, d.db)), want) {
		t.Fatal("fresh device did not restore the whole archive after reseed")
	}
	// Compare every plaintext segment, including the retired device's bytes.
	segments, err := filepath.Glob(filepath.Join(root, "devices", "*", "*.seg"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("local segments: %v", err)
	}
	for _, path := range segments {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		wantBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		gotBytes, err := os.ReadFile(filepath.Join(strings.TrimSuffix(d.db, ".duckdb")+".log", rel))
		if err != nil || !bytes.Equal(gotBytes, wantBytes) {
			t.Fatalf("restored segment differs: %s (%v)", rel, err)
		}
		cipher, err := os.ReadFile(filepath.Join(relayDir, "spaces", space.ID, strings.TrimSuffix(rel, ".seg")+".seg.age"))
		if err != nil || !bytes.HasPrefix(cipher, []byte("age-encryption.org/v1\n")) {
			t.Fatalf("relay segment is not age ciphertext: %s (%v)", rel, err)
		}
	}
	out = run(t, "sync", "--reseed", "--db", a.db)
	if !strings.Contains(out, "Uploaded: 0 segments  Downloaded: 0 segments") {
		t.Fatalf("idle reseed moved data: %s", out)
	}
}

func TestSetRelayRejectsInvalidURLAndUnpairedDevice(t *testing.T) {
	srv := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer srv.Close()
	a := newDevice(t, nil)
	if out, err := runErr("sync", "--set-relay", srv.URL, "--db", a.db); err == nil || !strings.Contains(err.Error(), "isn't set up to sync") || strings.Contains(out, "chatstrata-join:") {
		t.Fatalf("unpaired relay change: output=%q error=%v", out, err)
	}
	run(t, "pair", "--relay", srv.URL, "--db", a.db)
	path := filepath.Join(strings.TrimSuffix(a.db, ".duckdb")+".log", "sync.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runErr("sync", "--set-relay", "file:///tmp/relay", "--db", a.db); err == nil {
		t.Fatal("accepted invalid relay URL")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid relay URL changed config: %v", err)
	}
}
