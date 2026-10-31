// Package analysis holds the built-in archive analysis queries.
package analysis

import "embed"

//go:embed queries/*.sql
var Queries embed.FS
