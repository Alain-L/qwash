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

// Note: sql/btree_bloat.sql is NOT embedded here. It is a standalone query
// for DBA use (with pg_size_pretty formatting). The Go code uses its own
// inline query in analysis/index_bloat.go that returns raw int64/float64 values.
