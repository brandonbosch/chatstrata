// Package migrations holds schema migrations that only the Go implementation
// applies. They continue the numbering of the shared migrations in
// chatstrata/core/migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
