package sql

import (
	_ "embed"
)

//go:embed table_bloat.sql
var TableBloatSQL string

//go:embed compact_procedure.sql
var CompactProcedureSQL string
