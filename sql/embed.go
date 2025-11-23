package sql

import (
	_ "embed"
)

//go:embed table_bloat.sql
var TableBloatSQL string
