// Package chatstrata holds files shared between the Python and Go
// implementations during the rewrite, so there is one copy of each.
//
// After the Python code is removed (milestone M6) these move under internal/.
package chatstrata

import "embed"

// Migrations holds the schema migrations, named NNNN_description.sql.
//
//go:embed chatstrata/core/migrations/*.sql
var Migrations embed.FS

// AnalysisQueries holds the queries behind `chatstrata analyze`, named
// <subcommand>.sql, with Python str.format placeholders ({source_filter}, ...).
//
//go:embed chatstrata/analysis/queries/*.sql
var AnalysisQueries embed.FS
