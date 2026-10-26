package migrations

import "embed"

// Files is the append-only history of database DDL. Applied migration files
// must never be edited, renamed, deleted, or squashed.
//
//go:embed *.sql
var Files embed.FS
