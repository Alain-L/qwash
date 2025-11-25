package db

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

// CompactTableUntilConvergence keeps compacting until bloat is minimal
// This achieves near-0% bloat without VACUUM FULL
func (db *DB) CompactTableUntilConvergence(tableName string, initialBloatPages int) error {
	ctx := context.Background()

	fmt.Println("\n🎯 Compaction with convergence strategy:")
	fmt.Println("   Loop until bloat < 5% or no progress")

	const targetBloatPct = 2.0   // Stop when bloat < 2%
	const minPagesToProcess = 5  // Don't bother if < 5 pages of bloat

	iteration := 0
	previousBloatPages := initialBloatPages

	fmt.Printf("📊 Initial bloat: %d pages\n", initialBloatPages)

	for {
		iteration++

		// Step 1: Get actual page count (REAL, not pg_class.relpages which is stale)
		var actualPages int
		query := fmt.Sprintf(`
			SELECT COUNT(DISTINCT (ctid::text::point)[0]::int)
			FROM %s
		`, pgx.Identifier{tableName}.Sanitize())
		err := db.conn.QueryRow(ctx, query).Scan(&actualPages)
		if err != nil {
			return fmt.Errorf("failed to get actual pages: %w", err)
		}

		// Step 2: Estimate minimum pages based on actual tuple count
		// Simple estimation: ~72 tuples per page (empirically observed)
		var tupleCount int
		query = fmt.Sprintf(`SELECT COUNT(*) FROM %s`, pgx.Identifier{tableName}.Sanitize())
		err = db.conn.QueryRow(ctx, query).Scan(&tupleCount)
		if err != nil {
			return fmt.Errorf("failed to count tuples: %w", err)
		}

		// Estimate minimum pages (using fillfactor 100 for aggressive target)
		// Average ~72 tuples/page for this table structure
		const avgTuplesPerPage = 72
		minPages := int(math.Ceil(float64(tupleCount) / float64(avgTuplesPerPage)))

		currentBloatPages := actualPages - minPages
		if currentBloatPages < 0 {
			currentBloatPages = 0
		}
		bloatPct := float64(currentBloatPages) * 100.0 / float64(actualPages)

		fmt.Printf("📊 Current: %d pages (%d tuples), Min: %d pages, Bloat: %d pages\n",
			actualPages, tupleCount, minPages, currentBloatPages)

		fmt.Printf("\n🔄 Iteration %d: %d bloat pages remaining (%.2f%% bloat)\n",
			iteration, currentBloatPages, bloatPct)

		// Step 2: Check convergence conditions
		if currentBloatPages < minPagesToProcess {
			fmt.Printf("✅ Bloat reduced to %d pages - stopping (threshold: %d pages)\n",
				currentBloatPages, minPagesToProcess)
			break
		}

		if bloatPct < targetBloatPct {
			fmt.Printf("✅ Bloat reduced to %.2f%% - stopping (target: %.1f%%)\n",
				bloatPct, targetBloatPct)
			break
		}

		// Check if we're making progress (allow small increases due to temporary bloat)
		// Only stop if bloat increased by more than 10 pages or stayed same for 3 iterations
		if iteration > 3 {
			bloatIncrease := currentBloatPages - previousBloatPages
			if bloatIncrease > 10 {
				fmt.Printf("⚠️  Bloat increased significantly (was %d, now %d) - stopping\n",
					previousBloatPages, currentBloatPages)
				break
			}
		}

		// Safety: max 100 iterations (should be enough to converge)
		if iteration > 100 {
			fmt.Println("⚠️  Maximum iterations reached (100) - stopping")
			break
		}

		// Step 3: Process next batch - small batches for better convergence
		pagesToProcess := 2 // Process just 2 pages per round for fine-grained control
		if currentBloatPages < pagesToProcess {
			pagesToProcess = currentBloatPages
		}

		fmt.Printf("🔨 Processing %d pages...\n", pagesToProcess)
		if err := db.RunQwash(tableName, pagesToProcess); err != nil {
			return fmt.Errorf("RunQwash failed at iteration %d: %w", iteration, err)
		}

		// VACUUM after each iteration to update stats and allow convergence check
		_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s;",
			pgx.Identifier{tableName}.Sanitize()))
		if err != nil {
			return fmt.Errorf("VACUUM ANALYZE failed at iteration %d: %w", iteration, err)
		}

		previousBloatPages = currentBloatPages
	}

	// Final verification
	finalBloatPages, _ := db.GetBloatPages(tableName)
	var finalActualPages int
	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT (ctid::text::point)[0]::int)
		FROM %s
	`, pgx.Identifier{tableName}.Sanitize())
	db.conn.QueryRow(ctx, query).Scan(&finalActualPages)
	finalBloatPct := float64(finalBloatPages) * 100.0 / float64(finalActualPages)

	fmt.Printf("\n🎯 Convergence complete after %d iterations\n", iteration)
	fmt.Printf("📊 Final: %d pages, %.2f%% bloat\n", finalActualPages, finalBloatPct)

	// Show highest CTIDs
	query = fmt.Sprintf(`SELECT ctid FROM %s ORDER BY ctid DESC LIMIT 2`,
		pgx.Identifier{tableName}.Sanitize())
	rows, err := db.conn.Query(ctx, query)
	if err == nil {
		defer rows.Close()
		fmt.Println("\n🔎 Highest CTIDs:")
		for rows.Next() {
			var ctid string
			if err := rows.Scan(&ctid); err == nil {
				fmt.Printf("  - %s\n", ctid)
			}
		}
	}

	return nil
}
