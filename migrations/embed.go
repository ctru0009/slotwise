// Package migrations embeds the goose migration files so tests and the binary
// apply exactly the SQL that is committed.
package migrations

import "embed"

// FS holds the SQL migrations.
//
//go:embed *.sql
var FS embed.FS
