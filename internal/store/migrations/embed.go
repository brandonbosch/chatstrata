// Package migrations holds the archive schema, including the original
// migrations used by Python archives. Never renumber existing migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
