//go:build fts_embed

package store

import _ "embed"

// embeddedFTS is DuckDB's signed fts extension for this build's DuckDB
// version and platform, gzipped, put there by internal/tools/fetchfts.
//
//go:embed fts/fts.duckdb_extension.gz
var embeddedFTS []byte
