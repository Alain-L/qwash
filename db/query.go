package db

import (
	"context"
	"fmt"
	"qwash/sql"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Exec executes a query without returning rows.
func (db *DB) Exec(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	return db.conn.Exec(ctx, query, args...)
}

// sanitizeTableName properly quotes a table name that may include a schema prefix.
// E.g., "public.mytable" -> "public"."mytable", "mytable" -> "mytable"
func sanitizeTableName(tableName string) string {
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		return fmt.Sprintf("%s.%s", pgx.Identifier{parts[0]}.Sanitize(), pgx.Identifier{parts[1]}.Sanitize())
	}
	return pgx.Identifier{tableName}.Sanitize()
}


// printProgress prints a single-line progress that updates in place
func printProgress(tableName string, pass, round int, percent float64, remaining int, stagnation, maxStagnation, unblock, maxUnblock int, status string) {
	fmt.Printf("\r\033[K⏳ P%d R%d | %.0f%% | %d left | stag:%d/%d unblk:%d/%d | %s",
		pass, round, percent, remaining, stagnation, maxStagnation, unblock, maxUnblock, status)
}

// clearProgress clears the progress line and moves to next line
func clearProgress() {
	fmt.Print("\r\033[K")
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

// RunQwash is the core debloat operation.
// It moves tuples from high pages to lower pages using a single atomic transaction.
// The tuples are reinserted and will use any PRE-EXISTING free space in lower pages.
// VACUUM must be run AFTER this operation to reclaim the now-empty high pages.
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
			tx.Rollback(ctx)
		}
	}()

	// Step 1: Get CTIDs from the HIGHEST pages (by page number)
	// Targeting high pages allows VACUUM to truncate the file
	selectQuery := fmt.Sprintf(`
		SELECT ctid
		FROM %s
		WHERE (ctid::text::point)[0]::bigint IN (
			SELECT DISTINCT (ctid::text::point)[0]::bigint
			FROM %s
			ORDER BY (ctid::text::point)[0]::bigint DESC
			LIMIT %d
		)
	`, sanitizeTableName(tableName), sanitizeTableName(tableName), pageCount)

	rows, err := tx.Query(ctx, selectQuery)
	if err != nil {
		return fmt.Errorf("failed to fetch ctids: %w", err)
	}

	var ctids []string
	for rows.Next() {
		var ctid string
		if err := rows.Scan(&ctid); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan ctid: %w", err)
		}
		ctids = append(ctids, ctid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ctids) == 0 {
		return nil // No rows to process
	}

	// Build placeholder string for CTIDs
	placeholders := make([]string, len(ctids))
	args := make([]interface{}, len(ctids))
	for i, ctid := range ctids {
		placeholders[i] = fmt.Sprintf("$%d::tid", i+1)
		args[i] = ctid
	}
	inClause := strings.Join(placeholders, ", ")

	// Step 2: Create temp table with rows to move (dropped on commit)
	createTempQuery := fmt.Sprintf(`
		CREATE TEMPORARY TABLE qwash_tmp ON COMMIT DROP AS
		SELECT * FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, sanitizeTableName(tableName), inClause)

	if _, err := tx.Exec(ctx, createTempQuery, args...); err != nil {
		return fmt.Errorf("failed to create temp table: %w", err)
	}

	// Step 3: Delete from original table
	deleteQuery := fmt.Sprintf(`
		DELETE FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, sanitizeTableName(tableName), inClause)

	if _, err := tx.Exec(ctx, deleteQuery, args...); err != nil {
		return fmt.Errorf("failed to delete rows: %w", err)
	}

	// Step 4: Reinsert - tuples go to pre-existing free space (FSM) in lower pages
	reinsertQuery := fmt.Sprintf(`
		INSERT INTO %s SELECT * FROM qwash_tmp
	`, sanitizeTableName(tableName))

	if _, err := tx.Exec(ctx, reinsertQuery); err != nil {
		return fmt.Errorf("failed to reinsert rows: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
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
			tx.Rollback(ctx)
		}
	}()

	// Select CTIDs from the LEAST filled pages (by tuple count)
	// Emptying sparse pages allows VACUUM to reclaim them entirely
	selectQuery := fmt.Sprintf(`
		SELECT ctid
		FROM %s
		WHERE (ctid::text::point)[0]::bigint IN (
			SELECT (ctid::text::point)[0]::bigint
			FROM %s
			GROUP BY (ctid::text::point)[0]::bigint
			ORDER BY COUNT(*) ASC
			LIMIT %d
		)
	`, sanitizeTableName(tableName), sanitizeTableName(tableName), pageCount)

	rows, err := tx.Query(ctx, selectQuery)
	if err != nil {
		return fmt.Errorf("failed to fetch ctids from filled pages: %w", err)
	}

	var ctids []string
	for rows.Next() {
		var ctid string
		if err := rows.Scan(&ctid); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan ctid: %w", err)
		}
		ctids = append(ctids, ctid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ctids) == 0 {
		return nil // No rows to process
	}

	// Build placeholder string for CTIDs
	placeholders := make([]string, len(ctids))
	args := make([]interface{}, len(ctids))
	for i, ctid := range ctids {
		placeholders[i] = fmt.Sprintf("$%d::tid", i+1)
		args[i] = ctid
	}
	inClause := strings.Join(placeholders, ", ")

	// Create temp table with tuples from filled pages
	createTempQuery := fmt.Sprintf(`
		CREATE TEMPORARY TABLE qwash_tmp_filled ON COMMIT DROP AS
		SELECT * FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, sanitizeTableName(tableName), inClause)

	if _, err := tx.Exec(ctx, createTempQuery, args...); err != nil {
		return fmt.Errorf("failed to create temp table: %w", err)
	}

	// Delete from original table
	deleteQuery := fmt.Sprintf(`
		DELETE FROM %s WHERE ctid = ANY(ARRAY[%s])
	`, sanitizeTableName(tableName), inClause)

	if _, err := tx.Exec(ctx, deleteQuery, args...); err != nil {
		return fmt.Errorf("failed to delete rows: %w", err)
	}

	// Reinsert (will be placed in pages with free space via FSM)
	reinsertQuery := fmt.Sprintf(`
		INSERT INTO %s SELECT * FROM qwash_tmp_filled
	`, sanitizeTableName(tableName))

	if _, err := tx.Exec(ctx, reinsertQuery); err != nil {
		return fmt.Errorf("failed to reinsert rows: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
}

func (db *DB) CompactTable(tableName string, initialBloatPages int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	ctx := context.Background()

	// Set session_replication_role = replica to disable triggers during compaction
	_, err := db.conn.Exec(ctx, "SET session_replication_role = replica")
	if err != nil {
		return fmt.Errorf("failed to set session_replication_role: %w", err)
	}
	defer db.conn.Exec(ctx, "SET session_replication_role = DEFAULT")

	passNumber := 0
	totalRounds := 0
	stagnationCounter := 0
	previousBloat := initialBloatPages

	// Get initial actual page count
	initialActualPages, err := db.GetTablePages(tableName)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}

	bestActualPages := initialActualPages // Track the best (minimum) actual pages achieved
	passesWithoutImprovement := 0
	const maxStagnationPasses = 6 // Optimal: allows enough passes to achieve 100% VACUUM FULL efficiency
	const maxUnblockAttempts = 3 // Optimal: multiple unblock cycles for high bloat scenarios
	unblockAttempts := 0 // Count unblock attempts in this stagnation cycle

	fmt.Printf("\n🚀 Starting compaction for table '%s'\n", tableName)
	fmt.Printf("📦 Initial bloat: %d pages\n", initialBloatPages)

	// VACUUM once at the beginning to establish clean baseline
	fmt.Println("🧹 Running initial VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("initial VACUUM failed: %w", err)
	}

	// Outer loop: continue until no bloat remains
	for {
		passNumber++

		// Run ANALYZE to refresh statistics
		_, err := db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			clearProgress()
			return fmt.Errorf("ANALYZE failed at pass %d: %w", passNumber, err)
		}

		// Recalculate bloat and get actual pages
		bloatPages, err := db.GetBloatPages(tableName)
		if err != nil {
			clearProgress()
			return fmt.Errorf("failed to calculate bloat at pass %d: %w", passNumber, err)
		}

		// Get actual page count from pg_class for accurate tracking
		actualPages, err := db.GetTablePages(tableName)
		if err != nil {
			clearProgress()
			return fmt.Errorf("failed to get actual page count: %w", err)
		}

		// Track improvement based on actual page reduction, not just bloat estimation
		improved := actualPages < bestActualPages

		// Don't stop immediately when bloat estimation says 0
		if bloatPages <= 0 && !improved {
			bloatPages = 1 // Try one more small round
		}

		// Check if we improved the best result
		if improved {
			bestActualPages = actualPages
			passesWithoutImprovement = 0
			unblockAttempts = 0
		} else {
			passesWithoutImprovement++

			// Check if we should apply unblocking mechanism
			if passesWithoutImprovement >= maxStagnationPasses {
				if unblockAttempts < maxUnblockAttempts {
					unblockAttempts++

					// Unblock by redistributing ALL pages in chunks
					pagesLeft := actualPages
					chunkSize := 50
					unblockRound := 0
					for pagesLeft > 0 {
						if chunkSize > pagesLeft {
							chunkSize = pagesLeft
						}
						unblockRound++
						printProgress(tableName, passNumber, unblockRound, float64(actualPages-pagesLeft)/float64(actualPages)*100, pagesLeft,
							passesWithoutImprovement, maxStagnationPasses, unblockAttempts, maxUnblockAttempts, "Unblocking...")

						if err := db.RunQwashFilledPages(tableName, chunkSize); err != nil {
							clearProgress()
							return fmt.Errorf("unblock operation failed: %w", err)
						}
						pagesLeft -= chunkSize

						// VACUUM after each chunk
						_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
						if err != nil {
							clearProgress()
							return fmt.Errorf("VACUUM during unblock failed: %w", err)
						}
					}

					passesWithoutImprovement = 0
					previousBloat = bloatPages
					continue
				} else {
					break // Stop: unblock attempts exhausted
				}
			}
		}

		// Track stagnation
		if bloatPages >= previousBloat {
			stagnationCounter++
		} else {
			stagnationCounter = 0
		}
		previousBloat = bloatPages

		// Inner loop: degressive compaction for this pass
		pagesRemaining := bloatPages
		roundNumber := 0

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

			// Progress indicator (updates in place)
			percentDone := float64(bloatPages-pagesRemaining) / float64(bloatPages) * 100
			printProgress(tableName, passNumber, roundNumber+1, percentDone, pagesRemaining,
				passesWithoutImprovement, maxStagnationPasses, unblockAttempts, maxUnblockAttempts, "Compacting...")

			// Execute qwash on pagesThisRound pages
			if err := db.RunQwash(tableName, pagesThisRound); err != nil {
				clearProgress()
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, err)
			}

			pagesRemaining -= pagesThisRound
			roundNumber++
			totalRounds++

			// VACUUM after each round to:
			// 1. Reclaim space from deleted tuples (high pages become empty)
			// 2. Update FSM so next round's INSERT can use freed space
			_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
			if err != nil {
				clearProgress()
				return fmt.Errorf("VACUUM failed at pass %d, round %d: %w", passNumber, roundNumber, err)
			}
		}

		clearProgress()
		// Go to line 2, print pass completion, go back to line 1
		fmt.Print("\n")                                                                      // go to line 2
		fmt.Printf("\r\033[K✅ Pass %d done | %d rounds | %d pages", passNumber, roundNumber, actualPages) // update line 2
		fmt.Print("\033[A\r")                                                                // back to start of line 1

		// Run ANALYZE after each pass to update statistics for next bloat check
		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Final VACUUM to truncate empty trailing pages
	fmt.Print("\n\n") // go past line 2 to line 3
	fmt.Println("🧹 Running final VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	// Get final page count
	finalPages, _ := db.GetTablePages(tableName)
	fmt.Printf("🎯 Compaction: %d → %d pages (%d passes, %d rounds)\n",
		initialActualPages, finalPages, passNumber, totalRounds)
	return nil
}

// GetBloatPages calculates the number of bloated pages for a given table
// using estimation based on pg_stat_user_tables.
func (db *DB) GetBloatPages(tableName string) (int, error) {
	ctx := context.Background()

	// Handle schema-qualified table names (e.g., "public.mytable")
	var whereClause string
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		whereClause = fmt.Sprintf("WHERE schemaname = '%s' AND tblname = '%s'", parts[0], parts[1])
	} else {
		whereClause = fmt.Sprintf("WHERE tblname = '%s'", tableName)
	}

	// Inject the WHERE clause before the ORDER BY
	modifiedQuery := strings.Replace(
		sql.TableBloatSQL,
		"ORDER BY bloat_pct DESC;",
		whereClause+"\nORDER BY bloat_pct DESC;",
		1,
	)

	// Run the query
	row := db.conn.QueryRow(ctx, modifiedQuery)

	var (
		_           string   // table_name
		_           int      // live_tup
		_           int64    // dead_tup
		minPages    int
		actualPages int
		_           int      // fillfactor
		_           string   // relation_size
		_           string   // TOAST_size
		_           string   // bloat_size
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

	return actualPages - minPages, nil
}

// ListTablesFiltered returns tables filtered by schemas, system flag, and exclusion list.
func (db *DB) ListTablesFiltered(schemas []string, includeSystem bool, excludeTables []string) ([]string, error) {
	ctx := context.Background()

	// Build WHERE conditions
	var conditions []string

	// Filter by schemas if specified
	if len(schemas) > 0 {
		placeholders := make([]string, len(schemas))
		for i := range schemas {
			placeholders[i] = fmt.Sprintf("'%s'", schemas[i])
		}
		conditions = append(conditions, fmt.Sprintf("n.nspname IN (%s)", strings.Join(placeholders, ", ")))
	} else if !includeSystem {
		// Exclude system schemas by default
		conditions = append(conditions, "n.nspname NOT IN ('pg_catalog', 'pg_toast', 'information_schema')")
	}

	// Exclude specific tables
	if len(excludeTables) > 0 {
		placeholders := make([]string, len(excludeTables))
		for i := range excludeTables {
			placeholders[i] = fmt.Sprintf("'%s'", excludeTables[i])
		}
		conditions = append(conditions, fmt.Sprintf("c.relname NOT IN (%s)", strings.Join(placeholders, ", ")))
	}

	// Only regular tables (not indexes, sequences, etc.)
	conditions = append(conditions, "c.relkind = 'r'")

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT n.nspname || '.' || c.relname AS full_name
		FROM pg_class c
		JOIN pg_namespace n ON c.relnamespace = n.oid
		%s
		ORDER BY c.relname
	`, whereClause)

	rows, err := db.conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error querying tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("error scanning table name: %w", err)
		}
		tables = append(tables, tableName)
	}
	return tables, nil
}

// GetTablePages returns the number of pages for a table from pg_class.
func (db *DB) GetTablePages(tableName string) (int, error) {
	ctx := context.Background()

	// Handle schema-qualified table names
	var schemaName, relName string
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName = parts[0]
		relName = parts[1]
	} else {
		relName = tableName
	}

	var query string
	if schemaName != "" {
		query = fmt.Sprintf(`
			SELECT c.relpages
			FROM pg_class c
			JOIN pg_namespace n ON c.relnamespace = n.oid
			WHERE c.relname = '%s' AND n.nspname = '%s'
		`, relName, schemaName)
	} else {
		query = fmt.Sprintf("SELECT relpages FROM pg_class WHERE relname = '%s'", relName)
	}

	var pages int
	err := db.conn.QueryRow(ctx, query).Scan(&pages)
	if err != nil {
		return 0, fmt.Errorf("failed to get page count for '%s': %w", tableName, err)
	}
	return pages, nil
}

// CompactTableFast performs faster compaction with adaptive vacuum threshold (~99% efficiency).
// This is faster than CompactTable but may leave 1-2 pages of bloat on very large tables.
func (db *DB) CompactTableFast(tableName string, initialBloatPages int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	ctx := context.Background()

	// Set session_replication_role = replica to disable triggers during compaction
	_, err := db.conn.Exec(ctx, "SET session_replication_role = replica")
	if err != nil {
		return fmt.Errorf("failed to set session_replication_role: %w", err)
	}
	defer db.conn.Exec(ctx, "SET session_replication_role = DEFAULT")

	passNumber := 0
	totalRounds := 0
	stagnationCounter := 0
	previousBloat := initialBloatPages

	// Get initial actual page count
	initialActualPages, err := db.GetTablePages(tableName)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}

	bestActualPages := initialActualPages
	passesWithoutImprovement := 0
	const maxStagnationPasses = 3 // Fast: stop quickly if no progress
	const maxUnblockAttempts = 1  // Fast: one unblock attempt max
	const targetBloatPct = 10.0   // Fast: stop when bloat < 10%
	unblockAttempts := 0

	fmt.Printf("\n🚀 Starting FAST compaction for table '%s' (target: <%.0f%% bloat)\n", tableName, targetBloatPct)
	fmt.Printf("📦 Initial bloat: %d pages\n", initialBloatPages)

	// VACUUM once at the beginning
	fmt.Println("🧹 Running initial VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("initial VACUUM failed: %w", err)
	}

	// Outer loop: continue until no bloat remains
	for {
		passNumber++

		// Run ANALYZE to refresh statistics
		_, err := db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed at pass %d: %w", passNumber, err)
		}

		// Recalculate bloat and get actual pages
		bloatPages, err := db.GetBloatPages(tableName)
		if err != nil {
			return fmt.Errorf("failed to calculate bloat at pass %d: %w", passNumber, err)
		}

		// Get actual page count from pg_class
		actualPages, err := db.GetTablePages(tableName)
		if err != nil {
			return fmt.Errorf("failed to get actual page count: %w", err)
		}

		// Calculate current bloat percentage
		bloatPct := float64(bloatPages) / float64(actualPages) * 100
		if bloatPct < targetBloatPct {
			// Target reached, stop
			break
		}

		improved := actualPages < bestActualPages

		if bloatPages <= 0 && !improved {
			bloatPages = 1
		}

		if improved {
			bestActualPages = actualPages
			passesWithoutImprovement = 0
			unblockAttempts = 0
		} else {
			passesWithoutImprovement++

			if passesWithoutImprovement >= maxStagnationPasses {
				if unblockAttempts < maxUnblockAttempts {
					unblockAttempts++

					// Unblock by redistributing ALL pages in chunks
					pagesLeft := actualPages
					chunkSize := 50
					unblockRound := 0
					for pagesLeft > 0 {
						if chunkSize > pagesLeft {
							chunkSize = pagesLeft
						}
						unblockRound++
						printProgress(tableName, passNumber, unblockRound, float64(actualPages-pagesLeft)/float64(actualPages)*100, pagesLeft,
							passesWithoutImprovement, maxStagnationPasses, unblockAttempts, maxUnblockAttempts, "Unblocking...")

						if err := db.RunQwashFilledPages(tableName, chunkSize); err != nil {
							return fmt.Errorf("unblock operation failed: %w", err)
						}
						pagesLeft -= chunkSize

						// VACUUM after each chunk
						_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
						if err != nil {
							return fmt.Errorf("VACUUM during unblock failed: %w", err)
						}
					}

					passesWithoutImprovement = 0
					previousBloat = bloatPages
					continue
				} else {
					break
				}
			}
		}

		if bloatPages >= previousBloat {
			stagnationCounter++
		} else {
			stagnationCounter = 0
		}
		previousBloat = bloatPages

		// Inner loop: degressive compaction with adaptive VACUUM
		pagesRemaining := bloatPages
		roundNumber := 0
		roundsSinceLastVacuum := 0

		// Adaptive vacuum threshold: much less frequent VACUUM for speed
		getVacuumThreshold := func(remaining int) int {
			if remaining > 500 {
				return 20 // VACUUM every 20 rounds
			} else if remaining > 100 {
				return 10
			} else if remaining > 20 {
				return 5
			}
			return 2
		}

		for pagesRemaining > 0 {
			var pagesThisRound int
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

			if pagesThisRound > pagesRemaining {
				pagesThisRound = pagesRemaining
			}

			// Progress indicator (updates in place)
			percentDone := float64(bloatPages-pagesRemaining) / float64(bloatPages) * 100
			printProgress(tableName, passNumber, roundNumber+1, percentDone, pagesRemaining,
				passesWithoutImprovement, maxStagnationPasses, unblockAttempts, maxUnblockAttempts, "Compacting...")

			// Execute qwash
			if err := db.RunQwash(tableName, pagesThisRound); err != nil {
				clearProgress()
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, err)
			}

			pagesRemaining -= pagesThisRound
			roundNumber++
			totalRounds++
			roundsSinceLastVacuum++

			// Adaptive VACUUM: less frequent for speed, more frequent near end
			vacuumThreshold := getVacuumThreshold(pagesRemaining)
			if roundsSinceLastVacuum >= vacuumThreshold || pagesRemaining == 0 {
				_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
				if err != nil {
					clearProgress()
					return fmt.Errorf("VACUUM failed: %w", err)
				}
				roundsSinceLastVacuum = 0
			}
		}

		clearProgress()
		// Go to line 2, print pass completion, go back to line 1
		fmt.Print("\n")
		fmt.Printf("\r\033[K✅ Pass %d done | %d rounds | %d pages", passNumber, roundNumber, actualPages)
		fmt.Print("\033[A\r")

		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Final VACUUM
	fmt.Print("\n\n")
	fmt.Println("🧹 Running final VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	finalPages, _ := db.GetTablePages(tableName)
	fmt.Printf("🎯 FAST compaction: %d → %d pages (%d passes, %d rounds)\n",
		initialActualPages, finalPages, passNumber, totalRounds)
	return nil
}

// CompactTableSlow performs conservative compaction, processing 1 page at a time with delays.
// This approach is similar to pgcompacttable: very gentle on production systems.
func (db *DB) CompactTableSlow(tableName string, initialBloatPages int, delayMs int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	ctx := context.Background()
	delay := time.Duration(delayMs) * time.Millisecond

	// Set session_replication_role = replica to disable triggers during compaction
	_, err := db.conn.Exec(ctx, "SET session_replication_role = replica")
	if err != nil {
		return fmt.Errorf("failed to set session_replication_role: %w", err)
	}
	defer db.conn.Exec(ctx, "SET session_replication_role = DEFAULT")

	// Get initial actual page count
	initialActualPages, err := db.GetTablePages(tableName)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}

	fmt.Printf("\n🐢 Starting SLOW compaction for table '%s' (1 page at a time, %dms delay)\n", tableName, delayMs)
	fmt.Printf("📦 Initial bloat: %d pages\n", initialBloatPages)

	// VACUUM once at the beginning to establish clean baseline
	fmt.Println("🧹 Running initial VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("initial VACUUM failed: %w", err)
	}

	totalRounds := 0
	passNumber := 0
	bestActualPages := initialActualPages
	passesWithoutImprovement := 0
	const maxStagnationPasses = 6
	const maxUnblockAttempts = 3
	unblockAttempts := 0

	// Outer loop: continue until no progress
	for {
		passNumber++

		// Run ANALYZE to refresh statistics
		_, err := db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			clearProgress()
			return fmt.Errorf("ANALYZE failed at pass %d: %w", passNumber, err)
		}

		// Recalculate bloat
		bloatPages, err := db.GetBloatPages(tableName)
		if err != nil {
			clearProgress()
			return fmt.Errorf("failed to calculate bloat at pass %d: %w", passNumber, err)
		}

		actualPages, err := db.GetTablePages(tableName)
		if err != nil {
			clearProgress()
			return fmt.Errorf("failed to get actual page count: %w", err)
		}

		improved := actualPages < bestActualPages

		if bloatPages <= 0 && !improved {
			bloatPages = 1
		}

		if improved {
			bestActualPages = actualPages
			passesWithoutImprovement = 0
			unblockAttempts = 0
		} else {
			passesWithoutImprovement++

			if passesWithoutImprovement >= maxStagnationPasses {
				if unblockAttempts < maxUnblockAttempts {
					unblockAttempts++

					// Unblock by redistributing pages
					pagesLeft := actualPages
					unblockRound := 0
					for pagesLeft > 0 {
						unblockRound++
						printProgress(tableName, passNumber, unblockRound, float64(actualPages-pagesLeft)/float64(actualPages)*100, pagesLeft,
							passesWithoutImprovement, maxStagnationPasses, unblockAttempts, maxUnblockAttempts, "Unblocking...")

						if err := db.RunQwashFilledPages(tableName, 1); err != nil {
							clearProgress()
							return fmt.Errorf("unblock operation failed: %w", err)
						}
						pagesLeft--

						// VACUUM after each page
						_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
						if err != nil {
							clearProgress()
							return fmt.Errorf("VACUUM during unblock failed: %w", err)
						}

						// Delay between operations
						time.Sleep(delay)
					}

					passesWithoutImprovement = 0
					continue
				} else {
					break
				}
			}
		}

		// Inner loop: process 1 page at a time (like pgcompacttable)
		pagesRemaining := bloatPages
		roundNumber := 0

		for pagesRemaining > 0 {
			// Always process exactly 1 page (conservative approach)
			pagesThisRound := 1

			// Progress indicator
			percentDone := float64(bloatPages-pagesRemaining) / float64(bloatPages) * 100
			printProgress(tableName, passNumber, roundNumber+1, percentDone, pagesRemaining,
				passesWithoutImprovement, maxStagnationPasses, unblockAttempts, maxUnblockAttempts, "Compacting...")

			// Execute qwash on 1 page
			if err := db.RunQwash(tableName, pagesThisRound); err != nil {
				clearProgress()
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, err)
			}

			pagesRemaining -= pagesThisRound
			roundNumber++
			totalRounds++

			// VACUUM after each page
			_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
			if err != nil {
				clearProgress()
				return fmt.Errorf("VACUUM failed at pass %d, round %d: %w", passNumber, roundNumber, err)
			}

			// Delay between operations (like pgcompacttable)
			time.Sleep(delay)
		}

		clearProgress()
		fmt.Print("\n")
		fmt.Printf("\r\033[K✅ Pass %d done | %d rounds | %d pages", passNumber, roundNumber, actualPages)
		fmt.Print("\033[A\r")

		// Run ANALYZE after each pass
		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Final VACUUM
	fmt.Print("\n\n")
	fmt.Println("🧹 Running final VACUUM...")
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	finalPages, _ := db.GetTablePages(tableName)
	fmt.Printf("🎯 SLOW compaction: %d → %d pages (%d passes, %d rounds)\n",
		initialActualPages, finalPages, passNumber, totalRounds)
	return nil
}

// ReindexTable runs REINDEX CONCURRENTLY on a table to rebuild all its indexes.
func (db *DB) ReindexTable(tableName string) error {
	ctx := context.Background()

	// REINDEX CONCURRENTLY requires PostgreSQL 12+
	// It rebuilds indexes without blocking writes
	query := fmt.Sprintf("REINDEX TABLE CONCURRENTLY %s", sanitizeTableName(tableName))

	fmt.Printf("🔧 Running REINDEX CONCURRENTLY on %s...\n", tableName)
	_, err := db.conn.Exec(ctx, query)
	if err != nil {
		// If CONCURRENTLY fails (e.g., PG < 12), try regular REINDEX
		fmt.Println("⚠️  REINDEX CONCURRENTLY failed, trying regular REINDEX...")
		query = fmt.Sprintf("REINDEX TABLE %s", sanitizeTableName(tableName))
		_, err = db.conn.Exec(ctx, query)
		if err != nil {
			return fmt.Errorf("REINDEX failed: %w", err)
		}
	}

	fmt.Printf("✅ REINDEX complete for %s\n", tableName)
	return nil
}
