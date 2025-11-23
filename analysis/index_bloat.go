package analysis

import (
	"context"
	"fmt"
	"log"

	"qwash/db"
)

// DetectBtreeIndexBloat analyzes B-Tree indexes for bloat.
func DetectBtreeIndexBloat(ctx context.Context, dbConn *db.DB) ([]BloatIndex, error) {
	log.Println("[INFO] Analyzing B-Tree index bloat...")

	// Placeholder query (to be replaced with a real PostgreSQL index bloat query)
	query := `
		SELECT schemaname, indexname, tablename, pg_relation_size(indexname::regclass),
		       pg_relation_size(indexname::regclass) - pg_relation_size(tablename::regclass) AS bloat_size
		FROM pg_indexes
		LIMIT 10;
	`

	rows, err := dbConn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch index bloat data: %w", err)
	}
	defer rows.Close()

	var bloatIndexes []BloatIndex

	for rows.Next() {
		var idx BloatIndex
		err := rows.Scan(&idx.Schema, &idx.IndexName, &idx.TableName, &idx.IndexSize, &idx.BloatSize)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %w", err)
		}
		if idx.IndexSize > 0 {
			idx.BloatRatio = float64(idx.BloatSize) / float64(idx.IndexSize) * 100
		}
		bloatIndexes = append(bloatIndexes, idx)
	}

	log.Printf("[INFO] Found %d potentially bloated indexes.\n", len(bloatIndexes))
	return bloatIndexes, nil
}
