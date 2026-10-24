// Package migrations embeds the SQL migration set so the API, admin CLI, and
// tests all apply exactly the same files.
package migrations

import "embed"

// FS contains every versioned migration.
//
//go:embed *.sql
var FS embed.FS
