package db

import (
	"context"
	"fmt"
	"qwash/sql"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Exec executes a query without returning rows.
func (db *DB) Exec(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	return db.conn.Exec(ctx, query, args...)
}

// QueryRow executes a query that is expected to return a single row.
func (db *DB) QueryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	return db.conn.QueryRow(ctx, query, args...)
}

// Query executes a query that may return multiple rows.
func (db *DB) Query(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	return db.conn.Query(ctx, query, args...)
}

// ListDatabases retrieves all non-template databases from PostgreSQL.
func (db *DB) ListDatabases() ([]string, error) {
	rows, err := db.conn.Query(context.Background(),
		"SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname;")
	if err != nil {
		return nil, fmt.Errorf("error querying databases: %w", err)
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var dbname string
		if err := rows.Scan(&dbname); err != nil {
			return nil, fmt.Errorf("error scanning database name: %w", err)
		}
		databases = append(databases, dbname)
	}
	return databases, nil
}

// ListDatabases retrieves all non-template databases from PostgreSQL.
func (db *DB) ListTables() ([]string, error) {
	rows, err := db.conn.Query(context.Background(),
		"SELECT relname FROM pg_class c JOIN pg_namespace n ON c.relnamespace = n.oid WHERE n.nspname NOT IN ('pg_catalog', 'pg_toast', 'information_schema') ORDER BY c.relname;")
	if err != nil {
		return nil, fmt.Errorf("error querying databases: %w", err)
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var dbname string
		if err := rows.Scan(&dbname); err != nil {
			return nil, fmt.Errorf("error scanning database name: %w", err)
		}
		databases = append(databases, dbname)
	}
	return databases, nil
}

// RunQwash is the core debloat operation using embedded SQL templates
func (db *DB) RunQwash(tableName string, pageCount int) error {
	if pageCount <= 0 {
		return fmt.Errorf("invalid pageCount: must be > 0")
	}

	ctx := context.Background()
	tx, err := db.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			fmt.Println("⚠️  Rolling back transaction.")
			tx.Rollback(ctx)
		} else {
			tx.Commit(ctx)
		}
	}()

	// Step 1: Get CTIDs from the highest pages
	// Select actual CTIDs (physical row locations) not IDs
	selectQuery := fmt.Sprintf(`
		SELECT ctid
		FROM %s
		WHERE (ctid::text::point)[0]::bigint IN (
			SELECT DISTINCT (ctid::text::point)[0]::bigint
			FROM %s
			ORDER BY (ctid::text::point)[0]::bigint DESC
			LIMIT %d
		)
	`, pgx.Identifier{tableName}.Sanitize(), pgx.Identifier{tableName}.Sanitize(), pageCount)

	rows, err := tx.Query(ctx, selectQuery)
	if err != nil {
		return fmt.Errorf("failed to fetch ctids: %w", err)
	}
	defer rows.Close()

	var ctids []string
	for rows.Next() {
		var ctid string
		if err := rows.Scan(&ctid); err != nil {
			return fmt.Errorf("failed to scan ctid: %w", err)
		}
		ctids = append(ctids, ctid)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ctids) == 0 {
		return fmt.Errorf("no rows found to perform qwash")
	}

	// Build placeholder string like ($1, $2, ..., $N) for CTIDs
	placeholders := make([]string, len(ctids))
	args := make([]interface{}, len(ctids))
	for i, ctid := range ctids {
		placeholders[i] = fmt.Sprintf("$%d::tid", i+1)
		args[i] = ctid
	}
	inClause := strings.Join(placeholders, ", ")

	// Step 2: Create a temporary table with CTIDs
	createTempQuery := fmt.Sprintf(`
		CREATE TEMPORARY TABLE qwash_tmp ON COMMIT DROP AS
		SELECT * FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, pgx.Identifier{tableName}.Sanitize(), inClause)

	if _, err := tx.Exec(ctx, createTempQuery, args...); err != nil {
		return fmt.Errorf("failed to create temporary table: %w", err)
	}

	// Step 3: Delete from original table by CTID
	deleteQuery := fmt.Sprintf(`
		DELETE FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, pgx.Identifier{tableName}.Sanitize(), inClause)

	if _, err := tx.Exec(ctx, deleteQuery, args...); err != nil {
		return fmt.Errorf("failed to delete rows: %w", err)
	}

	// Step 4: Reinsert from temp
	reinsertQuery := fmt.Sprintf(`
		INSERT INTO %s SELECT * FROM qwash_tmp
	`, pgx.Identifier{tableName}.Sanitize())

	if _, err := tx.Exec(ctx, reinsertQuery); err != nil {
		return fmt.Errorf("failed to insert rows back: %w", err)
	}

	return nil
}

// RunQwashFilledPages empties the most filled pages (used for unblocking)
// This redistributes tuples from filled pages to pages with free space
func (db *DB) RunQwashFilledPages(tableName string, pageCount int) error {
	if pageCount <= 0 {
		return fmt.Errorf("invalid pageCount: must be > 0")
	}

	ctx := context.Background()
	tx, err := db.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			fmt.Println("⚠️  Rolling back transaction.")
			tx.Rollback(ctx)
		} else {
			tx.Commit(ctx)
		}
	}()

	// Select CTIDs from the most filled pages (by tuple count)
	selectQuery := fmt.Sprintf(`
		SELECT ctid
		FROM %s
		WHERE (ctid::text::point)[0]::bigint IN (
			SELECT (ctid::text::point)[0]::bigint
			FROM %s
			GROUP BY (ctid::text::point)[0]::bigint
			ORDER BY COUNT(*) DESC
			LIMIT %d
		)
	`, pgx.Identifier{tableName}.Sanitize(), pgx.Identifier{tableName}.Sanitize(), pageCount)

	rows, err := tx.Query(ctx, selectQuery)
	if err != nil {
		return fmt.Errorf("failed to fetch ctids from filled pages: %w", err)
	}
	defer rows.Close()

	var ctids []string
	for rows.Next() {
		var ctid string
		if err := rows.Scan(&ctid); err != nil {
			return fmt.Errorf("failed to scan ctid: %w", err)
		}
		ctids = append(ctids, ctid)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ctids) == 0 {
		return fmt.Errorf("no rows found in filled pages")
	}

	// Build placeholder string for CTIDs
	placeholders := make([]string, len(ctids))
	args := make([]interface{}, len(ctids))
	for i, ctid := range ctids {
		placeholders[i] = fmt.Sprintf("$%d::tid", i+1)
		args[i] = ctid
	}
	inClause := strings.Join(placeholders, ", ")

	// Create temporary table with tuples from filled pages
	createTempQuery := fmt.Sprintf(`
		CREATE TEMPORARY TABLE qwash_tmp_filled ON COMMIT DROP AS
		SELECT * FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, pgx.Identifier{tableName}.Sanitize(), inClause)

	if _, err := tx.Exec(ctx, createTempQuery, args...); err != nil {
		return fmt.Errorf("failed to create temporary table: %w", err)
	}

	// Delete from original table
	deleteQuery := fmt.Sprintf(`
		DELETE FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, pgx.Identifier{tableName}.Sanitize(), inClause)

	if _, err := tx.Exec(ctx, deleteQuery, args...); err != nil {
		return fmt.Errorf("failed to delete rows: %w", err)
	}

	// Reinsert (will be placed in pages with free space via FSM)
	reinsertQuery := fmt.Sprintf(`
		INSERT INTO %s SELECT * FROM qwash_tmp_filled
	`, pgx.Identifier{tableName}.Sanitize())

	if _, err := tx.Exec(ctx, reinsertQuery); err != nil {
		return fmt.Errorf("failed to insert rows back: %w", err)
	}

	return nil
}

func (db *DB) CompactTable(tableName string, initialBloatPages int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	ctx := context.Background()
	passNumber := 0
	totalRounds := 0
	stagnationCounter := 0
	previousBloat := initialBloatPages

	// Get initial actual page count
	var initialActualPages int
	err := db.conn.QueryRow(ctx, fmt.Sprintf(
		"SELECT relpages FROM pg_class WHERE relname = '%s'", tableName,
	)).Scan(&initialActualPages)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}

	bestActualPages := initialActualPages // Track the best (minimum) actual pages achieved
	passesWithoutImprovement := 0
	const maxStagnationPasses = 6 // Optimal: allows enough passes to achieve 100% VACUUM FULL efficiency
	const maxUnblockAttempts = 3 // Optimal: multiple unblock cycles for high bloat scenarios
	unblockAttempts := 0 // Count unblock attempts in this stagnation cycle

	fmt.Printf("\n🚀 Starting iterative degressive compaction for table '%s'\n", tableName)
	fmt.Printf("📦 Initial bloat: %d pages\n", initialBloatPages)

	// VACUUM once at the beginning to establish clean baseline
	fmt.Println("🧹 Running initial VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
	if err != nil {
		return fmt.Errorf("initial VACUUM failed: %w", err)
	}

	// Outer loop: continue until no bloat remains
	for {
		passNumber++
		fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("🔄 PASS %d: Checking bloat...\n", passNumber)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		// Run ANALYZE to refresh statistics
		_, err := db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
		if err != nil {
			return fmt.Errorf("ANALYZE failed at pass %d: %w", passNumber, err)
		}

		// Recalculate bloat and get actual pages
		bloatPages, err := db.GetBloatPages(tableName)
		if err != nil {
			return fmt.Errorf("failed to calculate bloat at pass %d: %w", passNumber, err)
		}

		// Get actual page count from pg_class for accurate tracking
		var actualPages int
		err = db.conn.QueryRow(ctx, fmt.Sprintf(
			"SELECT relpages FROM pg_class WHERE relname = '%s'", tableName,
		)).Scan(&actualPages)
		if err != nil {
			return fmt.Errorf("failed to get actual page count: %w", err)
		}

		// Track improvement based on actual page reduction, not just bloat estimation
		// This is more reliable as bloat estimation can be imprecise
		improved := actualPages < bestActualPages

		// Don't stop immediately when bloat estimation says 0
		// The estimation can be imprecise, so we rely on actual page count improvement
		if bloatPages <= 0 {
			fmt.Printf("ℹ️  Bloat estimation reports 0 pages (actual: %d pages, best: %d pages)\n",
				actualPages, bestActualPages)
			if !improved {
				// Only continue if we might still improve
				bloatPages = 1 // Try one more small round
			}
		}

		// Check if we improved the best result (based on actual pages, not estimated bloat)
		if improved {
			// New best result!
			fmt.Printf("🎯 New best result: %d actual pages (improved from %d, bloat est: %d)\n",
				actualPages, bestActualPages, bloatPages)
			bestActualPages = actualPages
			passesWithoutImprovement = 0
			unblockAttempts = 0 // Reset unblock counter on improvement
		} else {
			// No improvement over best result
			passesWithoutImprovement++
			fmt.Printf("⚠️  No improvement over best result (current: %d pages, best: %d pages). Passes without improvement: %d/%d\n",
				actualPages, bestActualPages, passesWithoutImprovement, maxStagnationPasses)

			// Check if we should apply unblocking mechanism
			if passesWithoutImprovement >= maxStagnationPasses {
				if unblockAttempts < maxUnblockAttempts {
					// Try unblocking: empty the most filled pages to redistribute tuples
					unblockAttempts++
					fmt.Printf("\n🔓 UNBLOCKING (attempt %d/%d): Emptying most filled pages to break stagnation...\n",
						unblockAttempts, maxUnblockAttempts)
					fmt.Printf("   (Will empty %d most filled pages)\n", bloatPages)

					// VACUUM before unblocking
					fmt.Println("🧹 Running VACUUM before unblocking...")
					_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
					if err != nil {
						return fmt.Errorf("VACUUM before unblock failed: %w", err)
					}

					// Empty the most filled pages
					if err := db.RunQwashFilledPages(tableName, bloatPages); err != nil {
						return fmt.Errorf("unblock operation failed: %w", err)
					}

					fmt.Println("✅ Unblocking complete. Resuming normal compaction...")
					passesWithoutImprovement = 0 // Reset counter to give it a chance after unblocking
					previousBloat = bloatPages
					continue // Skip to next pass
				} else {
					// Already tried maximum unblock attempts and still no improvement - stop
					fmt.Printf("\n🛑 Stopping: No improvement after %d passes (%d unblock attempts exhausted).\n",
						maxStagnationPasses, maxUnblockAttempts)
					fmt.Printf("   Best result achieved: %d actual pages (current: %d pages)\n", bestActualPages, actualPages)
					break
				}
			}
		}

		// Also track simple stagnation for old logic compatibility
		if bloatPages >= previousBloat {
			stagnationCounter++
		} else {
			stagnationCounter = 0
		}
		previousBloat = bloatPages

		fmt.Printf("📦 Bloat detected: %d pages → starting compaction pass %d\n", bloatPages, passNumber)

		// Inner loop: degressive compaction for this pass
		pagesRemaining := bloatPages
		roundNumber := 0
		roundsSinceLastVacuum := 0
		const vacuumThreshold = 1 // VACUUM every round for 100% precision

		for pagesRemaining > 0 {
			// Calculate pages to process this round (degressive, adaptive to total bloat)
			var pagesThisRound int
			if bloatPages > 10000 {
				// Very large tables: start with bigger chunks
				if pagesRemaining > 1000 {
					pagesThisRound = 100
				} else if pagesRemaining > 500 {
					pagesThisRound = 50
				} else if pagesRemaining > 100 {
					pagesThisRound = 20
				} else if pagesRemaining > 20 {
					pagesThisRound = 5
				} else if pagesRemaining > 5 {
					pagesThisRound = 2
				} else {
					pagesThisRound = 1
				}
			} else if bloatPages > 1000 {
				// Large tables
				if pagesRemaining > 500 {
					pagesThisRound = 50
				} else if pagesRemaining > 100 {
					pagesThisRound = 20
				} else if pagesRemaining > 20 {
					pagesThisRound = 5
				} else if pagesRemaining > 5 {
					pagesThisRound = 2
				} else {
					pagesThisRound = 1
				}
			} else {
				// Small/medium tables (original logic)
				if pagesRemaining > 500 {
					pagesThisRound = 20
				} else if pagesRemaining > 100 {
					pagesThisRound = 10
				} else if pagesRemaining > 20 {
					pagesThisRound = 5
				} else if pagesRemaining > 5 {
					pagesThisRound = 2
				} else {
					pagesThisRound = 1
				}
			}

			if pagesThisRound > pagesRemaining {
				pagesThisRound = pagesRemaining
			}

			// Execute qwash on pagesThisRound pages
			if err := db.RunQwash(tableName, pagesThisRound); err != nil {
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, err)
			}

			pagesRemaining -= pagesThisRound
			roundNumber++
			totalRounds++
			roundsSinceLastVacuum++

			// Progress indicator
			percentDone := float64(bloatPages-pagesRemaining) / float64(bloatPages) * 100
			fmt.Printf("⏳ Pass %d, Round %d: processed %d pages (%.1f%% of pass done, %d pages remaining)\n",
				passNumber, roundNumber, pagesThisRound, percentDone, pagesRemaining)

			// Conditional VACUUM: every round for 100% precision
			if roundsSinceLastVacuum >= vacuumThreshold || pagesRemaining == 0 {
				fmt.Printf("🧹 Running VACUUM after %d rounds...\n", roundsSinceLastVacuum)
				_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
				if err != nil {
					return fmt.Errorf("VACUUM failed at pass %d, round %d: %w", passNumber, roundNumber, err)
				}
				roundsSinceLastVacuum = 0
			}
		}

		fmt.Printf("✅ Pass %d complete: %d rounds executed\n", passNumber, roundNumber)

		// Run ANALYZE after each pass to update statistics for next bloat check
		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Final VACUUM to truncate empty trailing pages
	fmt.Println("\n🧹 Running final VACUUM to truncate empty pages...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	// 🔍 Display highest CTIDs at the end
	query := fmt.Sprintf(`SELECT ctid FROM %s ORDER BY ctid DESC LIMIT 2`, pgx.Identifier{tableName}.Sanitize())
	rows, err := db.conn.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to fetch ctids after compaction: %w", err)
	}
	defer rows.Close()

	fmt.Println("\n🔎 Highest CTIDs after final compaction:")
	for rows.Next() {
		var ctid string
		if err := rows.Scan(&ctid); err != nil {
			return fmt.Errorf("failed to scan ctid: %w", err)
		}
		fmt.Printf("  - %s\n", ctid)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating over ctid rows: %w", err)
	}

	fmt.Printf("\n🎯 Full compaction complete: %d passes, %d total rounds\n", passNumber-1, totalRounds)
	return nil
}

// getBloatPages calculates the number of bloated pages for a given table.
func (db *DB) GetBloatPages(tableName string) (int, error) {
	ctx := context.Background()

	// Inject the WHERE clause before the ORDER BY
	modifiedQuery := strings.Replace(
		sql.TableBloatSQL,
		"ORDER BY bloat_pct DESC;",
		fmt.Sprintf("WHERE tblname = '%s'\nORDER BY bloat_pct DESC;", tableName),
		1,
	)

	// Run the query
	row := db.conn.QueryRow(ctx, modifiedQuery)

	var (
		_           string  // table_name
		_           int     // live_tup
		_           int64   // dead_tup
		minPages    int
		actualPages int
		_           int     // fillfactor
		_           string  // relation_size
		_           string  // TOAST_size
		_           string  // bloat_size
		bloatPct    *float64 // bloat_pct (nullable)
	)

	err := row.Scan(
		new(string), // table_name
		new(int),    // live_tup
		new(int64),  // dead_tup
		&minPages,
		&actualPages,
		new(int),    // fillfactor
		new(string), // relation_size
		new(string), // TOAST_size
		new(string), // bloat_size
		&bloatPct,   // bloat_pct (nullable)
	)

	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("no bloat data found for table '%s'", tableName)
	}
	if err != nil {
		return 0, fmt.Errorf("error querying bloat info: %w", err)
	}

	diff := actualPages - minPages
	fmt.Printf("📦 %d bloat pages (%d - %d) estimated for table %s.\n", diff, actualPages, minPages, tableName)
	return diff, nil
}
