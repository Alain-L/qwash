package analysis

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"qwash/db"
	"qwash/sql"
)

// DetectTableBloat analyzes table bloat in PostgreSQL using the embedded bloat query.
func DetectTableBloat(ctx context.Context, dbConn *db.DB) ([]BloatTable, error) {
	slog.Info("Analyzing table bloat...")

	rows, err := dbConn.Query(ctx, sql.TableBloatSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch table bloat data: %w", err)
	}
	defer rows.Close()

	var bloatTables []BloatTable

	for rows.Next() {
		var (
			tableName   string
			liveTup     int
			deadTup     int64
			minPages    int
			actualPages int
			fillfactor  int
			tableSize   int64 // relation_size, raw bytes
			toastSize   int64 // TOAST_size, raw bytes (unused for now)
			bloatSize   int64 // bloat_size, raw bytes
			bloatPct    *float64
		)

		err := rows.Scan(
			&tableName,
			&liveTup,
			&deadTup,
			&minPages,
			&actualPages,
			&fillfactor,
			&tableSize,
			&toastSize,
			&bloatSize,
			&bloatPct,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %w", err)
		}
		_ = toastSize // not surfaced in BloatTable yet

		// Parse table_name format "schema.table"
		parts := strings.Split(tableName, ".")
		var schema, table string
		if len(parts) == 2 {
			schema = parts[0]
			table = parts[1]
		} else {
			schema = "public"
			table = tableName
		}

		// Handle NULL bloatPct
		var bloatRatio float64
		if bloatPct != nil {
			bloatRatio = *bloatPct
		}

		tbl := BloatTable{
			Schema:     schema,
			TableName:  table,
			TableSize:  tableSize,
			BloatSize:  bloatSize,
			BloatRatio: bloatRatio,
			Pages:      actualPages,
			MinPages:   minPages,
			LiveTuples: int64(liveTup),
			DeadTuples: deadTup,
			FillFactor: fillfactor,
		}

		bloatTables = append(bloatTables, tbl)
	}

	slog.Info("Table bloat analysis complete", "tables", len(bloatTables))
	return bloatTables, nil
}
