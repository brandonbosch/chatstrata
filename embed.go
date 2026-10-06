// Package chatstrata holds files shared between the Python and Go
// implementations during the rewrite, so there is one copy of each.
//
// After the Python code is removed (milestone M5) these move under internal/.
package chatstrata

import "embed"

// Migrations holds the schema migrations, named NNNN_description.sql.
//
//go:embed chatstrata/core/migrations/*.sql
var Migrations embed.FS
