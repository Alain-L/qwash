package db

import (
	"context"
	"fmt"
	"hash/crc32"
	"strings"
	"time"

	"qwash/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Safety thresholds
const (
	// MaxTransactionAgeMinutes is the max age of transactions before warning
	MaxTransactionAgeMinutes = 30
	// LockWaitTimeoutSeconds is how long to wait for locks before failing
	LockWaitTimeoutSeconds = 5
)

// checkTableLocks verifies no conflicting locks exist on the table.
// Returns an error if ACCESS EXCLUSIVE or SHARE locks are held.
func (db *DB) checkTableLocks(tableName string) error {
	ctx := context.Background()

	// Parse schema and table name
	var schemaName, relName string
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName = parts[0]
		relName = parts[1]
	} else {
		schemaName = "public"
		relName = tableName
	}

	// Check for conflicting locks (ACCESS EXCLUSIVE, SHARE, SHARE ROW EXCLUSIVE)
	query := `
		SELECT l.mode, a.usename, a.application_name,
		       extract(epoch from (now() - a.query_start))::int as duration_sec
		FROM pg_locks l
		JOIN pg_class c ON l.relation = c.oid
		JOIN pg_namespace n ON c.relnamespace = n.oid
		LEFT JOIN pg_stat_activity a ON l.pid = a.pid
		WHERE n.nspname = $1 AND c.relname = $2
		  AND l.mode IN ('AccessExclusiveLock', 'ShareLock', 'ShareRowExclusiveLock', 'ExclusiveLock')
		  AND l.granted = true
		LIMIT 1
	`

	var lockMode, userName, appName string
	var durationSec int
	err := db.QueryRow(ctx, query, schemaName, relName).Scan(&lockMode, &userName, &appName, &durationSec)

	if err == nil {
		// Found a conflicting lock
		return fmt.Errorf("table '%s' has conflicting lock: %s (held by %s/%s for %ds)",
			tableName, lockMode, userName, appName, durationSec)
	}
	if err.Error() != "no rows in result set" {
		return fmt.Errorf("failed to check locks: %w", err)
	}

	return nil
}

// checkLongTransactions checks for transactions older than threshold.
// These can block VACUUM from reclaiming space.
// Returns a warning message if found, empty string otherwise.
func (db *DB) checkLongTransactions() (string, error) {
	ctx := context.Background()

	query := `
		SELECT usename, application_name,
		       extract(epoch from (now() - xact_start))/60 as age_minutes,
		       state
		FROM pg_stat_activity
		WHERE xact_start IS NOT NULL
		  AND pid != pg_backend_pid()
		  AND extract(epoch from (now() - xact_start))/60 > $1
		ORDER BY xact_start
		LIMIT 3
	`

	rows, err := db.conn.Query(ctx, query, MaxTransactionAgeMinutes)
	if err != nil {
		return "", fmt.Errorf("failed to check transactions: %w", err)
	}
	defer rows.Close()

	var warnings []string
	for rows.Next() {
		var userName, appName, state string
		var ageMinutes float64
		if err := rows.Scan(&userName, &appName, &ageMinutes, &state); err != nil {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s/%s (%.0fmin, %s)", userName, appName, ageMinutes, state))
	}

	if len(warnings) > 0 {
		return fmt.Sprintf("long-running transactions may block VACUUM: %s", strings.Join(warnings, ", ")), nil
	}
	return "", nil
}

// setLockTimeout sets statement_timeout for lock acquisition.
// This prevents waiting indefinitely for locks.
func (db *DB) setLockTimeout(ctx context.Context) error {
	_, err := db.conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = '%ds'", LockWaitTimeoutSeconds))
	return err
}

// resetLockTimeout resets the lock timeout to default.
func (db *DB) resetLockTimeout(ctx context.Context) {
	db.conn.Exec(ctx, "SET lock_timeout = 0")
}

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

// getUpdatableColumn finds the first updatable column for a table.
// Returns the column name or error if none found.
// Excludes: generated columns, dropped columns, system columns.
func (db *DB) getUpdatableColumn(tableName string) (string, error) {
	ctx := context.Background()

	// Handle schema-qualified names
	var schemaName, relName string
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName = parts[0]
		relName = parts[1]
	} else {
		schemaName = "public"
		relName = tableName
	}

	// Find best column for UPDATE compaction (like pgcompacttable):
	// 1. Prefer NON-INDEXED columns (avoid index maintenance)
	// 2. Prefer FIXED-LENGTH types (avoid TOAST overhead)
	// 3. Prefer smaller types (less I/O)
	query := `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON a.atttypid = t.oid
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		  AND COALESCE(a.attgenerated, '') = ''
		  AND NOT EXISTS (
		      SELECT 1 FROM pg_index i
		      WHERE i.indrelid = c.oid
		        AND a.attnum = ANY(i.indkey)
		  )
		ORDER BY
		    -- Prefer fixed-length types (typlen > 0) over variable-length (typlen = -1)
		    CASE WHEN t.typlen > 0 THEN 0 ELSE 1 END,
		    -- Then prefer smaller types
		    t.typlen,
		    -- Finally by column order
		    a.attnum
		LIMIT 1
	`

	var columnName string
	err := db.QueryRow(ctx, query, schemaName, relName).Scan(&columnName)

	// If no non-indexed column found, fall back to any column
	if err == pgx.ErrNoRows {
		fallbackQuery := `
			SELECT a.attname
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1
			  AND c.relname = $2
			  AND a.attnum > 0
			  AND NOT a.attisdropped
			  AND COALESCE(a.attgenerated, '') = ''
			ORDER BY a.attnum
			LIMIT 1
		`
		err = db.QueryRow(ctx, fallbackQuery, schemaName, relName).Scan(&columnName)
	}

	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("no updatable column found for table '%s'", tableName)
	}
	if err != nil {
		return "", fmt.Errorf("error finding updatable column: %w", err)
	}
	return columnName, nil
}

// CompactTableUpdate uses the UPDATE SET col=col method to compact tables.
// It uses bloat estimation to determine the target page count,
// processing only the estimated bloated pages instead of the full table.
// This approach is efficient and may require 1-2 passes for complete compaction.
func (db *DB) CompactTableUpdate(tableName string) error {
	ctx := context.Background()

	// Safety check: verify no conflicting locks on table
	if err := db.checkTableLocks(tableName); err != nil {
		return err
	}

	// Safety check: warn about long-running transactions
	if warning, err := db.checkLongTransactions(); err != nil {
		return fmt.Errorf("failed to check transactions: %w", err)
	} else if warning != "" && db.Verbose {
		fmt.Printf("  Warning: %s\n", warning)
	}

	// Set lock timeout to avoid waiting indefinitely
	if err := db.setLockTimeout(ctx); err != nil {
		return fmt.Errorf("failed to set lock timeout: %w", err)
	}
	defer db.resetLockTimeout(ctx)

	// Acquire advisory lock to prevent concurrent compaction
	locked, err := db.acquireTableLock(tableName)
	if err != nil {
		return fmt.Errorf("failed to check table lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("table '%s' is already being compacted by another qwash process", tableName)
	}
	defer func() {
		if unlockErr := db.releaseTableLock(tableName); unlockErr != nil {
			if db.Verbose {
				fmt.Printf("  Warning: failed to release lock for %s: %v\n", tableName, unlockErr)
			}
		}
	}()

	// Find an updatable column (prefer non-indexed, fixed-length)
	column, err := db.getUpdatableColumn(tableName)
	if err != nil {
		return err
	}

	// Set session_replication_role = replica to disable triggers
	_, err = db.conn.Exec(ctx, "SET session_replication_role = replica")
	if err != nil {
		return fmt.Errorf("failed to set session_replication_role: %w", err)
	}
	defer db.conn.Exec(ctx, "SET session_replication_role = DEFAULT")

	// Get initial stats
	initialPages, err := db.GetTablePages(tableName)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}

	// Calculate to_page (target minimum pages) using bloat estimation
	bloatPages, err := db.GetBloatPages(tableName)
	if err != nil {
		return fmt.Errorf("failed to estimate bloat: %w", err)
	}
	toPage := initialPages - bloatPages
	if toPage < 0 {
		toPage = 0
	}

	if db.Verbose {
		fmt.Printf("Compacting '%s' using UPDATE method (column: %s)...\n", tableName, column)
		fmt.Printf("  Initial: %d pages, target: %d pages, bloat: %d pages\n",
			initialPages, toPage, bloatPages)
	}

	// Create stored procedure from embedded SQL
	procName := fmt.Sprintf("qwash_compact_w%d_%d", db.WorkerID, time.Now().UnixNano())
	createProc := fmt.Sprintf(sql.CompactProcedureSQL, procName)

	_, err = db.conn.Exec(ctx, createProc)
	if err != nil {
		return fmt.Errorf("failed to create procedure: %w", err)
	}
	defer db.conn.Exec(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s(text,text,integer,integer,integer)", procName))

	// Initial VACUUM to establish baseline
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("initial VACUUM failed: %w", err)
	}

	// Parameters
	pagesPerRound := 1
	pagesBeforeVacuum := initialPages / 16
	if pagesBeforeVacuum < 1 {
		pagesBeforeVacuum = 1
	}
	maxTuplesPerPage := 226 // Conservative estimate for 8KB pages

	// Prepare table and column identifiers
	tableIdent := sanitizeTableName(tableName)
	columnIdent := pgx.Identifier{column}.Sanitize()

	// Main loop: process pages from end towards toPage
	pagesProcessed := 0
	pagesSinceVacuum := 0
	currentPage := initialPages - 1 // Start from last page (0-indexed)

	for currentPage > toPage {
		// Call procedure to process current page
		var resultPage int
		err = db.QueryRow(ctx,
			fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5)", procName),
			tableIdent, columnIdent, currentPage, pagesPerRound, maxTuplesPerPage,
		).Scan(&resultPage)

		if err != nil {
			return fmt.Errorf("procedure call failed at page %d: %w", currentPage, err)
		}

		if resultPage == -1 && db.Verbose {
			fmt.Printf("\n  Warning: tuples moved to higher page at page %d\n", currentPage)
		} else if resultPage == -2 && db.Verbose {
			fmt.Printf("\n  Warning: max loops reached at page %d\n", currentPage)
		}

		pagesProcessed += pagesPerRound
		pagesSinceVacuum += pagesPerRound
		currentPage -= pagesPerRound

		// Progress display
		if !db.SilentProgress {
			fmt.Printf("\r\033[K  Page %d/%d | vacuum in %d",
				currentPage, toPage, pagesBeforeVacuum-pagesSinceVacuum)
		}

		// VACUUM periodically
		if pagesSinceVacuum >= pagesBeforeVacuum {
			_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s", sanitizeTableName(tableName)))
			if err != nil {
				return fmt.Errorf("VACUUM failed at page %d: %w", currentPage, err)
			}
			pagesSinceVacuum = 0

			if db.Verbose {
				currentPages, _ := db.GetTablePages(tableName)
				fmt.Printf("\r\033[K  VACUUM at page %d, current size: %d pages\n",
					currentPage, currentPages)
			}
		}
	}

	// Clear progress line
	if !db.SilentProgress {
		fmt.Print("\r\033[K")
	}

	// Final VACUUM
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	// Final ANALYZE
	_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final ANALYZE failed: %w", err)
	}

	// Report results
	finalPages, _ := db.GetTablePages(tableName)
	if db.Verbose {
		fmt.Printf("  Done: %d -> %d pages (%d pages processed)\n",
			initialPages, finalPages, pagesProcessed)
	}

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
		_           string // table_name
		_           int    // live_tup
		_           int64  // dead_tup
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

// acquireTableLock tries to acquire an advisory lock for table compaction.
// Returns true if lock was acquired, false if already locked by another session.
// Uses CRC32 hash of table name to generate consistent lock ID.
func (db *DB) acquireTableLock(tableName string) (bool, error) {
	lockID := int64(crc32.ChecksumIEEE([]byte(tableName)))

	query := "SELECT pg_try_advisory_lock($1)"
	var locked bool
	err := db.QueryRow(context.Background(), query, lockID).Scan(&locked)
	if err != nil {
		return false, fmt.Errorf("failed to acquire advisory lock: %w", err)
	}

	return locked, nil
}

// releaseTableLock releases the advisory lock for a table.
func (db *DB) releaseTableLock(tableName string) error {
	lockID := int64(crc32.ChecksumIEEE([]byte(tableName)))

	_, err := db.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
	if err != nil {
		return fmt.Errorf("failed to release advisory lock: %w", err)
	}

	return nil
}

// ReindexTable runs REINDEX CONCURRENTLY on a table to rebuild all its indexes.
func (db *DB) ReindexTable(tableName string) error {
	ctx := context.Background()

	// REINDEX CONCURRENTLY requires PostgreSQL 12+
	// It rebuilds indexes without blocking writes
	query := fmt.Sprintf("REINDEX TABLE CONCURRENTLY %s", sanitizeTableName(tableName))

	if db.Verbose {
		fmt.Printf("  Reindexing %s...\n", tableName)
	}
	_, err := db.conn.Exec(ctx, query)
	if err != nil {
		// If CONCURRENTLY fails (e.g., PG < 12), try regular REINDEX
		if db.Verbose {
			fmt.Println("  REINDEX CONCURRENTLY failed, trying regular REINDEX...")
		}
		query = fmt.Sprintf("REINDEX TABLE %s", sanitizeTableName(tableName))
		_, err = db.conn.Exec(ctx, query)
		if err != nil {
			return fmt.Errorf("REINDEX failed: %w", err)
		}
	}

	return nil
}
