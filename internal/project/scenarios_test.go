package project

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/obslog"
	"github.com/brandonbosch/chatstrata/internal/sources/claudecode"
	"github.com/brandonbosch/chatstrata/internal/store"
)

// The merge rules in spec/projection/scenarios.json, run against the
// projector. Each scenario lists events, the observations devices made of
// them, and the archive every device must end up with.

type scenarioObs struct {
	Device      string   `json:"device"`
	Seq         uint64   `json:"seq"`
	Source      string   `json:"source"`
	Scope       string   `json:"scope"`
	Locator     string   `json:"locator"`
	Kind        string   `json:"kind"`
	OffsetLines int      `json:"offset_lines"`
	Lines       []string `json:"lines"`
}

type expectedConv struct {
	Source   string            `json:"source"`
	Scope    string            `json:"scope"`
	Locator  string            `json:"locator"`
	Messages []string          `json:"messages"`
	Parents  map[string]string `json:"parents"`
}

type expectedDivergence struct {
	Source    string              `json:"source"`
	Locator   string              `json:"locator"`
	ForkAfter string              `json:"fork_after"`
	Branches  map[string][]string `json:"branches"`
}

type scenario struct {
	Name         string          `json:"name"`
	Events       json.RawMessage `json:"events"`
	Observations []scenarioObs   `json:"observations"`
	AllOrders    bool            `json:"check_all_delivery_orders"`
	Expected     struct {
		Conversations []expectedConv `json:"conversations"`
		Hidden        []struct {
			Source  string `json:"source"`
			Locator string `json:"locator"`
		} `json:"hidden"`
		Divergences []expectedDivergence `json:"divergences"`
	} `json:"expected"`
}

var ported = map[string]model.Source{"claude_code": claudecode.Source{}}

func TestScenarios(t *testing.T) {
	raw, err := os.ReadFile("../../spec/projection/scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, sc := range spec.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			for _, o := range sc.Observations {
				if o.Scope != "" {
					t.Skip("account scopes aren't part of conversation ids yet (see docs/rewrite/observation-log.md)")
				}
				if ported[o.Source] == nil {
					t.Skipf("needs the %s adapter", o.Source)
				}
			}
			order, lines := orderedEvents(t, sc.Events)
			orders := [][]int{identity(len(sc.Observations))}
			if sc.AllOrders {
				orders = permutations(len(sc.Observations))
			}
			for _, ord := range orders {
				t.Run(fmt.Sprint(ord), func(t *testing.T) {
					runScenario(t, sc, ord, order, lines)
				})
			}
		})
	}
}

// orderedEvents serializes each event as one JSONL line and returns the
// events' ids in file order (the order they're listed in the spec).
func orderedEvents(t *testing.T, raw json.RawMessage) ([]string, map[string][]byte) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var order []string
	lines := map[string][]byte{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var event json.RawMessage
		if err := dec.Decode(&event); err != nil {
			t.Fatal(err)
		}
		var compact bytes.Buffer
		json.Compact(&compact, event)
		id := tok.(string)
		order = append(order, id)
		lines[id] = append(compact.Bytes(), '\n')
	}
	return order, lines
}

func runScenario(t *testing.T, sc scenario, deliveryOrder []int, fileOrder []string, lines map[string][]byte) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "archive.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := obslog.Open(filepath.Join(dir, "archive.log"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Projector{Store: s, Log: l, Sources: ported}

	for _, i := range deliveryOrder {
		so := sc.Observations[i]
		o := &obslog.Observation{
			Device: so.Device, Seq: so.Seq, Source: so.Source, Scope: so.Scope, Locator: so.Locator,
			Kind: obslog.Kind(so.Kind), Path: "/transcripts/" + so.Locator + ".jsonl",
		}
		for _, id := range so.Lines {
			o.Content = append(o.Content, lines[id]...)
		}
		if o.Kind == obslog.Append {
			for _, id := range fileOrder[:so.OffsetLines] {
				o.Offset += int64(len(lines[id]))
			}
		}
		if err := l.WriteSegment(so.Device, []*obslog.Observation{o}); err != nil {
			t.Fatal(err)
		}
		// Project after every delivery, as a device would.
		if stats, err := p.CatchUp(ctx, false, io.Discard); err != nil || stats.Failed > 0 {
			t.Fatalf("catch-up: %v (failed %d)", err, stats.Failed)
		}
	}

	db := s.Conn()
	var count int
	db.QueryRowContext(ctx, `SELECT count(*) FROM conversations`).Scan(&count)
	if count != len(sc.Expected.Conversations) {
		t.Errorf("%d conversations, want %d", count, len(sc.Expected.Conversations))
	}
	for _, want := range sc.Expected.Conversations {
		got, parents := messagesOf(t, db, want.Source, want.Locator)
		if !reflect.DeepEqual(got, want.Messages) {
			t.Errorf("%s messages = %v, want %v", want.Locator, got, want.Messages)
		}
		for child, parent := range want.Parents {
			if parents[child] != parent {
				t.Errorf("parent of %s = %q, want %q", child, parents[child], parent)
			}
		}
	}
	for _, h := range sc.Expected.Hidden {
		dead, err := s.IsTombstoned(ctx, obslog.Key{Source: h.Source, Locator: h.Locator})
		if err != nil || !dead {
			t.Errorf("%s not tombstoned", h.Locator)
		}
	}
	checkDivergences(t, db, sc.Expected.Divergences)
}

func messagesOf(t *testing.T, db *sql.Conn, source, locator string) ([]string, map[string]string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT m.source_native_id, p.source_native_id
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		LEFT JOIN messages p ON p.id = m.parent_message_id
		WHERE c.source_id = ? AND c.source_native_id = ?
		ORDER BY m.sequence_index`, source, locator)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	parents := map[string]string{}
	for rows.Next() {
		var id string
		var parent sql.NullString
		if err := rows.Scan(&id, &parent); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		parents[id] = parent.String
	}
	return ids, parents
}

func checkDivergences(t *testing.T, db *sql.Conn, want []expectedDivergence) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT c.source_id, c.source_native_id, d.fork_after, d.branch_message, d.devices::VARCHAR
		FROM divergences d JOIN conversations c ON c.id = d.conversation_id
		ORDER BY 1, 2, 4`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]*expectedDivergence{}
	for rows.Next() {
		var source, locator, branch, devices string
		var fork sql.NullString
		if err := rows.Scan(&source, &locator, &fork, &branch, &devices); err != nil {
			t.Fatal(err)
		}
		k := source + "/" + locator
		if got[k] == nil {
			got[k] = &expectedDivergence{Source: source, Locator: locator, ForkAfter: fork.String, Branches: map[string][]string{}}
		}
		var list []string
		json.Unmarshal([]byte(devices), &list)
		got[k].Branches[branch] = list
	}
	if len(got) != len(want) {
		t.Errorf("divergences = %v, want %v", keys(got), want)
		return
	}
	for _, w := range want {
		g := got[w.Source+"/"+w.Locator]
		if g == nil || g.ForkAfter != w.ForkAfter || !reflect.DeepEqual(g.Branches, w.Branches) {
			t.Errorf("divergence for %s = %+v, want %+v", w.Locator, g, w)
		}
	}
}

func keys(m map[string]*expectedDivergence) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func permutations(n int) [][]int {
	var out [][]int
	var rec func(prefix []int, rest []int)
	rec = func(prefix, rest []int) {
		if len(rest) == 0 {
			out = append(out, append([]int(nil), prefix...))
			return
		}
		for i := range rest {
			next := append(append([]int(nil), rest[:i]...), rest[i+1:]...)
			rec(append(prefix, rest[i]), next)
		}
	}
	rec(nil, identity(n))
	return out
}
