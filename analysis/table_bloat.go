package analysis

import (
	"context"
	"fmt"
	"log"

	"qwash/db"
)

// DetectTableBloat analyzes table bloat in PostgreSQL.
func DetectTableBloat(ctx context.Context, dbConn *db.DB) ([]BloatTable, error) {
	log.Println("[INFO] Analyzing table bloat...")

	// Placeholder query (to be replaced with a real PostgreSQL table bloat query)
	query := `
		SELECT schemaname, tablename, pg_table_size(tablename::regclass),
		       pg_table_size(tablename::regclass) - pg_relation_size(tablename::regclass) AS bloat_size
		FROM pg_tables
		LIMIT 10;
	`

	rows, err := dbConn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch table bloat data: %w", err)
	}
	defer rows.Close()

	var bloatTables []BloatTable

	for rows.Next() {
		var tbl BloatTable
		err := rows.Scan(&tbl.Schema, &tbl.TableName, &tbl.TableSize, &tbl.BloatSize)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %w", err)
		}
		if tbl.TableSize > 0 {
			tbl.BloatRatio = float64(tbl.BloatSize) / float64(tbl.TableSize) * 100
		}
		bloatTables = append(bloatTables, tbl)
	}

	log.Printf("[INFO] Found %d potentially bloated tables.\n", len(bloatTables))
	return bloatTables, nil
}
