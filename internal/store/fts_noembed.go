//go:build !fts_embed

package store

// embeddedFTS is empty in development builds: search uses an extension
// installed with `reindex --install-fts`. Release builds embed it; see
// fts_embed.go.
var embeddedFTS []byte
