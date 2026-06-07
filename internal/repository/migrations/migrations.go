package migrations

import "embed"

// FS contains SQL migrations used by both the daemon and cmd/migrate.
//
//go:embed *.sql
var FS embed.FS
