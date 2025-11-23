package maintenance

import (
	"context"
	"fmt"
	"log"
	"time"

	"qwash/db"
)

// CompactTable reduces bloat by forcing page reuse in PostgreSQL without copying the data.
func CompactTable(ctx context.Context, dbConn *db.DB, schema, table, updateColumn string) error {
	qualifiedTable := fmt.Sprintf("%s.%s", schema, table)

	// Set session_replication_role to 'replica' to force tuple rewrites.
	_, err := dbConn.Exec(ctx, "SET session_replication_role TO replica")
	if err != nil {
		return fmt.Errorf("failed to set session_replication_role: %w", err)
	}
	log.Println("[INFO] Set session_replication_role to 'replica'.")

	const maxIterations = 100
	const batchPages = 5

	for iteration := 1; iteration <= maxIterations; iteration++ {
		// Get table size, page count, and estimated live tuples.
		var size int64
		var relpages int
		var reltuples float64
		query := fmt.Sprintf(
			"SELECT pg_relation_size('%s'::regclass), relpages, reltuples FROM pg_class WHERE oid = '%s'::regclass",
			qualifiedTable, qualifiedTable,
		)
		err := dbConn.QueryRow(ctx, query).Scan(&size, &relpages, &reltuples)
		if err != nil {
			return fmt.Errorf("failed to get table stats: %w", err)
		}
		log.Printf("[INFO] Iteration %d - Table: %s, Size: %d bytes, Pages: %d, Tuples: %.0f",
			iteration, qualifiedTable, size, relpages, reltuples)

		// Determine page range for update.
		targetPage := relpages - batchPages + 1
		if targetPage < 1 {
			targetPage = 1
		}

		// Execute forced updates to rewrite tuples.
		updateQuery := fmt.Sprintf(
			"UPDATE %s SET %s = %s WHERE ctid >= ((%d,0))::tid AND ctid < ((%d,0))::tid RETURNING 1",
			qualifiedTable, updateColumn, updateColumn, targetPage, targetPage+batchPages,
		)
		log.Printf("[INFO] Updating rows in pages [%d, %d)...", targetPage, targetPage+batchPages)

		rows, err := dbConn.Query(ctx, updateQuery)
		if err != nil {
			return fmt.Errorf("failed to execute update: %w", err)
		}
		updatedRows := 0
		for rows.Next() {
			var dummy int
			if err := rows.Scan(&dummy); err != nil {
				rows.Close()
				return fmt.Errorf("failed to scan update result: %w", err)
			}
			updatedRows++
		}
		rows.Close()
		log.Printf("[INFO] Iteration %d - Updated %d rows.", iteration, updatedRows)

		// Stop if no more updates occurred.
		if updatedRows == 0 {
			log.Println("[INFO] No more updates possible, assuming table is fully compacted.")
			break
		}

		// Perform a regular VACUUM to reclaim space.
		_, err = dbConn.Exec(ctx, fmt.Sprintf("VACUUM %s", qualifiedTable))
		if err != nil {
			return fmt.Errorf("failed to execute VACUUM: %w", err)
		}
		log.Printf("[INFO] VACUUM executed on %s.", qualifiedTable)

		time.Sleep(2 * time.Second) // Allow VACUUM to process.

		// Check if the table size has decreased.
		var newSize int64
		err = dbConn.QueryRow(ctx, fmt.Sprintf("SELECT pg_relation_size('%s'::regclass)", qualifiedTable)).Scan(&newSize)
		if err != nil {
			return fmt.Errorf("failed to get new table size: %w", err)
		}
		log.Printf("[INFO] New table size after iteration %d: %d bytes", iteration, newSize)

		// Stop if no space reduction occurred.
		if newSize >= size {
			log.Printf("[INFO] Table size stabilized (old: %d, new: %d). Compaction complete.", size, newSize)
			break
		}
	}

	// Reset session_replication_role to default.
	_, err = dbConn.Exec(ctx, "SET session_replication_role TO DEFAULT")
	if err != nil {
		log.Printf("[WARNING] Failed to reset session_replication_role: %v", err)
	} else {
		log.Println("[INFO] Reset session_replication_role to DEFAULT.")
	}

	return nil
}

// max returns the maximum of two integers.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
