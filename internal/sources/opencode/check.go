package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/sources/srcutil"
)

// Kinds the adapter reads or deliberately skips; anything else is new.
var (
	knownTablesV1  = []string{"session", "message", "part"}
	knownTablesV2  = []string{"session_v2", "session_message"}
	knownPartTypes = setOf("text", "reasoning", "tool", "patch", "step-start", "step-finish")
	knownTurnTypes = setOf("user", "assistant", "system", "idle", "agent-switched", "model-switched", "shell", "synthetic")
	knownItemTypes = setOf("text", "reasoning", "tool", "patch")
)

// newerLayout matches versioned session tables (session_v3, ...).
var newerLayout = regexp.MustCompile(`^session_v[0-9]+$`)

func setOf(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Check looks for OpenCode storage this adapter can't read: no session
// tables it knows, session tables from a newer layout, and record types it
// skips.
func (Source) Check(path string) (problems, notes []string) {
	path, err := srcutil.Root(path, DefaultPath)
	if err != nil {
		return []string{err.Error()}, nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := srcutil.OpenSQLite(path)
	if err != nil {
		return []string{fmt.Sprintf("can't open %s: %s", path, err)}, nil
	}
	defer db.Close()
	ctx := context.Background()
	tables, err := tableNames(ctx, db)
	if err != nil {
		return []string{fmt.Sprintf("can't list tables in %s: %s", path, err)}, nil
	}
	hasV1 := tables["session"] && tables["message"] && tables["part"]
	hasV2 := tables["session_v2"] && tables["session_message"]
	if !hasV1 && !hasV2 {
		problems = append(problems, fmt.Sprintf("%s has none of the session tables this version reads (%s or %s): OpenCode's storage format may have changed",
			path, strings.Join(knownTablesV2, "/"), strings.Join(knownTablesV1, "/")))
	}
	var unknownTables []string
	for name := range tables {
		if newerLayout.MatchString(name) && name != "session_v2" {
			unknownTables = append(unknownTables, name)
		}
	}
	if len(unknownTables) > 0 {
		sort.Strings(unknownTables)
		problems = append(problems, fmt.Sprintf("%s has session tables from a newer layout (%s): sessions in them aren't read", path, strings.Join(unknownTables, ", ")))
	}

	report := func(what, query string, known map[string]bool) {
		rows, err := srcutil.QueryRows(ctx, db, query)
		if err != nil {
			return
		}
		var unknown []string
		for _, r := range rows {
			if t, ok := r.Get("t").(string); ok && !known[t] {
				unknown = append(unknown, t)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			notes = append(notes, fmt.Sprintf("skips OpenCode %s it doesn't know: %s", what, strings.Join(unknown, ", ")))
		}
	}
	if hasV1 {
		report("1.x part types", `SELECT DISTINCT json_extract(data, '$.type') AS t FROM part`, knownPartTypes)
	}
	if hasV2 {
		report("2.x message types", `SELECT DISTINCT type AS t FROM session_message`, knownTurnTypes)
		report("2.x content types", `SELECT DISTINCT json_extract(j.value, '$.type') AS t
			FROM session_message m, json_each(m.data, '$.content') j WHERE m.type = 'assistant'`, knownItemTypes)
	}
	return problems, notes
}
