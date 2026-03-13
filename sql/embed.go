package sql

import (
	_ "embed"
)

//go:embed table_bloat.sql
var TableBloatSQL string

//go:embed compact_procedure.sql
var CompactProcedureSQL string

// Note: sql/toast_bloat.sql is NOT embedded here. It is a standalone query
// for DBA use (DO block + cursor requiring no installation). The Go code
// uses its own approach via a pg_temp helper function in analysis/toast_bloat.go.
