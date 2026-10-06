package srcutil

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"

	_ "github.com/mattn/go-sqlite3" // SQLite driver for database-backed sources
)

// OpenSQLite opens a source's SQLite database read-only, so collecting can
// never change the live store.
func OpenSQLite(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Row is a database row as an ordered JSON object, so a row serializes
// with its columns in table order, as Python's dict(row) does. BLOB columns
// are dropped, as they aren't JSON.
type Row struct {
	cols []string
	vals []any
}

// Get returns a column's value, or nil when the row has no such column.
func (r Row) Get(col string) any {
	for i, c := range r.cols {
		if c == col {
			return r.vals[i]
		}
	}
	return nil
}

func (r Row) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, c := range r.cols {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(c)
		v, err := json.Marshal(r.vals[i])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// QueryRows runs a query and returns its rows.
func QueryRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]Row, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := Row{}
		for i, v := range vals {
			if _, blob := v.([]byte); blob {
				continue
			}
			r.cols = append(r.cols, cols[i])
			r.vals = append(r.vals, v)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	return out, nil
}
