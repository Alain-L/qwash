package db

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"time"

	"github.com/Alain-L/qwash/sql"

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

	// Check for conflicting locks (ACCESS EXCLUSIVE, SHARE, SHARE ROW EXCLUSIVE).
	// Session info columns are coalesced: the LEFT JOIN yields NULLs for lock
	// holders without a pg_stat_activity entry (e.g. prepared transactions).
	query := `
		SELECT l.mode, coalesce(a.usename, '?'), coalesce(a.application_name, '?'),
		       coalesce(extract(epoch from (now() - a.query_start))::int, 0) as duration_sec
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
	if !errors.Is(err, pgx.ErrNoRows) {
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

// setLockTimeout sets lock_timeout for the session.
// This prevents waiting indefinitely for locks.
func (db *DB) setLockTimeout(ctx context.Context) error {
	_, err := db.conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = '%ds'", LockWaitTimeoutSeconds))
	return err
}

// resetLockTimeout restores the lock timeout to its default. RESET (rather
// than SET ... = 0) preserves any value configured server-side.
func (db *DB) resetLockTimeout(ctx context.Context) {
	db.conn.Exec(ctx, "RESET lock_timeout")
}

// Exec executes a query without returning rows.
func (db *DB) Exec(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	return db.conn.Exec(ctx, query, args...)
}

// ResolveTableName resolves a possibly-unqualified table name to its
// canonical "schema.table" form. Unqualified names are resolved through the
// session search_path — the same rule the compaction DML follows — so the
// bloat estimation, the advisory lock and the UPDATEs all target the same
// relation even when homonym tables exist in several schemas.
func (db *DB) ResolveTableName(tableName string) (string, error) {
	ctx := context.Background()

	var schemaName, relName string
	var err error
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		err = db.QueryRow(ctx, `
			SELECT n.nspname, c.relname
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'r'
		`, parts[0], parts[1]).Scan(&schemaName, &relName)
	} else {
		// Mimic search_path resolution for a bare name: first match in
		// current_schemas() order wins, exactly like the DML will resolve it.
		err = db.QueryRow(ctx, `
			SELECT n.nspname, c.relname
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = $1 AND c.relkind = 'r'
			  AND n.nspname = ANY(current_schemas(false))
			ORDER BY array_position(current_schemas(false), n.nspname)
			LIMIT 1
		`, tableName).Scan(&schemaName, &relName)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("table '%s' does not exist (or is not a regular table)", tableName)
	}
	if err != nil {
		return "", fmt.Errorf("failed to resolve table '%s': %w", tableName, err)
	}

	return schemaName + "." + relName, nil
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
	if errors.Is(err, pgx.ErrNoRows) {
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

	if errors.Is(err, pgx.ErrNoRows) {
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
	resolved, err := db.ResolveTableName(tableName)
	if err != nil {
		return err
	}
	toPage, err := db.estimateTargetPages(resolved)
	if err != nil {
		return err
	}
	return db.compactToTarget(context.Background(), resolved, toPage)
}

// CompactTableToTarget compacts an already-targeted table down to toPage,
// skipping the internal bloat estimation. The orchestrator uses this with a
// target precomputed once from GetAllBloatPages, instead of re-running the
// full-catalog bloat query for every table (and every compaction pass).
//
// The context cancels the compaction between page rounds (and aborts the
// in-flight statement), so an interrupted run stops promptly and cleanly.
func (db *DB) CompactTableToTarget(ctx context.Context, tableName string, toPage int) error {
	resolved, err := db.ResolveTableName(tableName)
	if err != nil {
		return err
	}
	return db.compactToTarget(ctx, resolved, toPage)
}

// estimateTargetPages returns the target page count (actual pages minus
// estimated bloat pages) for an already-resolved table.
func (db *DB) estimateTargetPages(tableName string) (int, error) {
	initialPages, err := db.GetTablePages(tableName)
	if err != nil {
		return 0, fmt.Errorf("failed to get initial page count: %w", err)
	}
	bloatPages, err := db.GetBloatPages(tableName)
	if err != nil {
		return 0, fmt.Errorf("failed to estimate bloat: %w", err)
	}
	toPage := initialPages - bloatPages
	if toPage < 0 {
		toPage = 0
	}
	return toPage, nil
}

// compactToTarget runs the UPDATE-based compaction on an already-resolved
// table down to the given target page count. The context cancels the loop
// between page rounds and aborts the in-flight statement.
func (db *DB) compactToTarget(ctx context.Context, tableName string, toPage int) error {
	if toPage < 0 {
		toPage = 0
	}

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

	// Get the current page count (a cheap pg_class lookup) to know where the
	// page loop starts; the target page count was supplied by the caller.
	initialPages, err := db.GetTablePages(tableName)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}
	if toPage > initialPages {
		toPage = initialPages
	}

	if db.Verbose {
		fmt.Printf("Compacting '%s' using UPDATE method (column: %s)...\n", tableName, column)
		fmt.Printf("  Initial: %d pages, target: %d pages, bloat: %d pages\n",
			initialPages, toPage, initialPages-toPage)
	}

	// Create stored procedure from embedded SQL. It lives in pg_temp so it is
	// dropped automatically with the session: an interrupted run (Ctrl-C,
	// network loss) leaves no orphan function behind, and no CREATE privilege
	// on a regular schema is needed.
	procName := fmt.Sprintf("pg_temp.qwash_compact_w%d_%d", db.WorkerID, time.Now().UnixNano())
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
		// Stop promptly on cancellation (Ctrl-C): the page just processed is
		// already committed, so the table stays consistent.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("compaction of '%s' interrupted at page %d: %w", tableName, currentPage, err)
		}

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

		// Throttle between page rounds (--slow mode): gives the server room
		// to breathe and keeps replication lag under control.
		if db.DelayMs > 0 {
			time.Sleep(time.Duration(db.DelayMs) * time.Millisecond)
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

	// Handle schema-qualified table names (e.g., "public.mytable").
	// Values are passed as query parameters, never interpolated.
	var whereClause string
	var args []any
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		whereClause = "WHERE schemaname = $1 AND tblname = $2"
		args = []any{parts[0], parts[1]}
	} else {
		whereClause = "WHERE tblname = $1"
		args = []any{tableName}
	}

	// Inject the WHERE clause before the ORDER BY. The marker must be present
	// exactly once: a silent no-op Replace (e.g. after an edit of the embedded
	// SQL) would run the query unfiltered and return the most bloated table of
	// the whole database instead of the requested one.
	const orderByMarker = "ORDER BY bloat_pct DESC;"
	if n := strings.Count(sql.TableBloatSQL, orderByMarker); n != 1 {
		return 0, fmt.Errorf("embedded table_bloat.sql contains %d occurrence(s) of marker %q (expected 1); refusing to run the bloat query unfiltered", n, orderByMarker)
	}
	modifiedQuery := strings.Replace(
		sql.TableBloatSQL,
		orderByMarker,
		whereClause+"\n"+orderByMarker,
		1,
	)

	// Run the query
	row := db.conn.QueryRow(ctx, modifiedQuery, args...)

	var (
		minPages    int
		actualPages int
		bloatPct    *float64 // nullable
	)

	// Only min/actual pages are needed here; the other columns are scanned
	// into throwaway destinations to satisfy the row shape.
	err := row.Scan(
		new(string), // table_name
		new(int),    // live_tup
		new(int64),  // dead_tup
		&minPages,
		&actualPages,
		new(int),   // fillfactor
		new(int64), // relation_size (bytes)
		new(int64), // TOAST_size (bytes)
		new(int64), // bloat_size (bytes)
		&bloatPct,  // bloat_pct (nullable)
		new(bool),  // stale_stats
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("no bloat data found for table '%s'", tableName)
	}
	if err != nil {
		return 0, fmt.Errorf("error querying bloat info: %w", err)
	}

	return actualPages - minPages, nil
}

// GetAllBloatPages runs the bloat estimation query a single time for the whole
// database and returns a map of "schema.table" -> bloat pages. The embedded
// query already computes every table on each call, so building the map once
// and looking results up avoids re-running the catalog-wide scan once per
// table (and per compaction pass), which was quadratic on large databases.
func (db *DB) GetAllBloatPages() (map[string]int, error) {
	ctx := context.Background()

	rows, err := db.conn.Query(ctx, sql.TableBloatSQL)
	if err != nil {
		return nil, fmt.Errorf("error querying bloat info: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var (
			tableName   string
			minPages    int
			actualPages int
		)
		if err := rows.Scan(
			&tableName,
			new(int),   // live_tup
			new(int64), // dead_tup
			&minPages,
			&actualPages,
			new(int),      // fillfactor
			new(int64),    // relation_size (bytes)
			new(int64),    // TOAST_size (bytes)
			new(int64),    // bloat_size (bytes)
			new(*float64), // bloat_pct (nullable)
			new(bool),     // stale_stats
		); err != nil {
			return nil, fmt.Errorf("error scanning bloat row: %w", err)
		}
		result[tableName] = actualPages - minPages
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading bloat rows: %w", err)
	}
	return result, nil
}

// DebloatPreflight inspects a (resolved) table for conditions that make
// compaction unsafe or surprising. A non-nil error means compaction must not
// proceed; warnings are non-fatal advisories the caller should surface.
//
// Hard errors:
//   - the current role can neither VACUUM the table (not owner, not superuser):
//     freed pages would never be reclaimed, so compaction would only add bloat;
//   - the table is in a logical-replication publication but has no usable
//     REPLICA IDENTITY: the compaction UPDATEs would fail.
//
// Warnings:
//   - ENABLE ALWAYS / ENABLE REPLICA triggers, which fire on every moved row
//     even under session_replication_role = replica;
//   - membership in a publication, which makes compaction generate WAL and
//     logical-replication traffic proportional to the data moved.
func (db *DB) DebloatPreflight(tableName string) (warnings []string, err error) {
	ctx := context.Background()

	schemaName, relName := "public", tableName
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName, relName = parts[0], parts[1]
	}

	var (
		canReclaim      bool
		ownerName       string
		alwaysReplTrigs int
		replIdent       string // 'd' default, 'n' nothing, 'f' full, 'i' index
		hasPK           bool
	)
	err = db.QueryRow(ctx, `
		SELECT
		  current_setting('is_superuser')::bool OR pg_has_role(current_user, c.relowner, 'USAGE'),
		  pg_get_userbyid(c.relowner),
		  (SELECT count(*) FROM pg_trigger t
		     WHERE t.tgrelid = c.oid AND NOT t.tgisinternal
		       AND t.tgenabled IN ('A', 'R')),
		  c.relreplident::text,
		  EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
	`, schemaName, relName).Scan(&canReclaim, &ownerName, &alwaysReplTrigs, &replIdent, &hasPK)
	if err != nil {
		return nil, fmt.Errorf("failed to run debloat preflight for '%s': %w", tableName, err)
	}

	if !canReclaim {
		return nil, fmt.Errorf("the current role can neither own nor superuser-VACUUM '%s' (owned by %q); compaction would move rows but VACUUM could not reclaim the pages, increasing bloat — run as the owner or a superuser",
			tableName, ownerName)
	}

	if alwaysReplTrigs > 0 {
		warnings = append(warnings, fmt.Sprintf("%s has ENABLE ALWAYS/REPLICA trigger(s) that will fire on every moved row", tableName))
	}

	// Publication membership (logical replication), PostgreSQL 10+ only.
	if db.ServerVersionNum() >= 100000 {
		var published bool
		if e := db.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_publication WHERE puballtables)
			    OR EXISTS (
			         SELECT 1 FROM pg_publication_rel pr
			         JOIN pg_class c ON c.oid = pr.prrelid
			         JOIN pg_namespace n ON n.oid = c.relnamespace
			         WHERE n.nspname = $1 AND c.relname = $2)
		`, schemaName, relName).Scan(&published); e == nil && published {
			// 'd' (default) needs a primary key; 'n' (nothing) is always unusable.
			if replIdent == "n" || (replIdent == "d" && !hasPK) {
				return warnings, fmt.Errorf("%s is in a logical-replication publication but has no usable REPLICA IDENTITY; the compaction UPDATEs would fail — set a REPLICA IDENTITY (e.g. a primary key or FULL)", tableName)
			}
			warnings = append(warnings, fmt.Sprintf("%s is in a logical-replication publication; compaction will generate WAL and replication traffic proportional to the data moved", tableName))
		}
	}

	return warnings, nil
}

// ListTablesFiltered returns tables filtered by schemas, system flag, and exclusion list.
// Returned names are schema-qualified ("schema.table"). Exclusions match
// either the bare table name or its qualified form. All user-provided values
// are passed as query parameters, never interpolated.
func (db *DB) ListTablesFiltered(schemas []string, includeSystem bool, excludeTables []string) ([]string, error) {
	ctx := context.Background()

	// Build WHERE conditions
	var conditions []string
	var args []any

	// Filter by schemas if specified
	if len(schemas) > 0 {
		args = append(args, schemas)
		conditions = append(conditions, fmt.Sprintf("n.nspname = ANY($%d)", len(args)))
	} else if !includeSystem {
		// Exclude system schemas by default
		conditions = append(conditions, "n.nspname NOT IN ('pg_catalog', 'pg_toast', 'information_schema')")
	}

	// Exclude specific tables (bare or schema-qualified names)
	if len(excludeTables) > 0 {
		args = append(args, excludeTables)
		conditions = append(conditions, fmt.Sprintf(
			"NOT (c.relname = ANY($%d) OR n.nspname || '.' || c.relname = ANY($%d))",
			len(args), len(args)))
	}

	// Only regular tables (not indexes, sequences, etc.)
	conditions = append(conditions, "c.relkind = 'r'")

	query := fmt.Sprintf(`
		SELECT n.nspname || '.' || c.relname AS full_name
		FROM pg_class c
		JOIN pg_namespace n ON c.relnamespace = n.oid
		WHERE %s
		ORDER BY c.relname
	`, strings.Join(conditions, " AND "))

	rows, err := db.conn.Query(ctx, query, args...)
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

	var row pgx.Row
	if schemaName != "" {
		row = db.conn.QueryRow(ctx, `
			SELECT c.relpages
			FROM pg_class c
			JOIN pg_namespace n ON c.relnamespace = n.oid
			WHERE c.relname = $1 AND n.nspname = $2
		`, relName, schemaName)
	} else {
		row = db.conn.QueryRow(ctx, "SELECT relpages FROM pg_class WHERE relname = $1", relName)
	}

	var pages int
	if err := row.Scan(&pages); err != nil {
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

// ReindexTable rebuilds a table's indexes with REINDEX TABLE CONCURRENTLY,
// which does not block writes. It deliberately does NOT fall back to a plain
// (blocking) REINDEX: that would contradict the tool's non-blocking promise.
//
// REINDEX CONCURRENTLY requires PostgreSQL 12+; on older servers the function
// refuses rather than silently taking an exclusive lock. When a CONCURRENTLY
// run fails (deadlock, timeout, cancellation), PostgreSQL can leave transient
// invalid indexes behind (suffixed _ccnew/_ccold); these are cleaned up so
// they don't accumulate.
func (db *DB) ReindexTable(ctx context.Context, tableName string) error {
	if db.ServerVersionNum() < 120000 {
		return fmt.Errorf("--reindex needs REINDEX CONCURRENTLY (PostgreSQL 12+); refusing to run a blocking REINDEX on '%s'", tableName)
	}

	if db.Verbose {
		fmt.Printf("  Reindexing %s...\n", tableName)
	}
	_, err := db.conn.Exec(ctx, fmt.Sprintf("REINDEX TABLE CONCURRENTLY %s", sanitizeTableName(tableName)))
	if err != nil {
		// No blocking fallback. Clean up any invalid leftovers from the failed
		// concurrent rebuild so they don't pile up across runs.
		if cleaned := db.dropInvalidReindexLeftovers(ctx, tableName); cleaned != "" {
			return fmt.Errorf("REINDEX CONCURRENTLY failed (%w); cleaned up leftover invalid index(es): %s", err, cleaned)
		}
		return fmt.Errorf("REINDEX CONCURRENTLY failed: %w", err)
	}

	return nil
}

// dropInvalidReindexLeftovers drops invalid transient indexes (named
// *_ccnew/*_ccold) left on a table by a failed REINDEX ... CONCURRENTLY.
// It returns a comma-separated list of the indexes it dropped (empty if none).
// Errors while dropping are ignored: this is best-effort cleanup.
func (db *DB) dropInvalidReindexLeftovers(ctx context.Context, tableName string) string {
	schemaName, relName := "public", tableName
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName, relName = parts[0], parts[1]
	}

	rows, err := db.conn.Query(ctx, `
		SELECT ns.nspname, ic.relname
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_class tc ON tc.oid = i.indrelid
		JOIN pg_namespace ns ON ns.oid = ic.relnamespace
		JOIN pg_namespace tn ON tn.oid = tc.relnamespace
		WHERE NOT i.indisvalid
		  AND tn.nspname = $1 AND tc.relname = $2
		  AND (ic.relname LIKE '%\_ccnew' OR ic.relname LIKE '%\_ccold'
		       OR ic.relname ~ '_ccnew[0-9]+$' OR ic.relname ~ '_ccold[0-9]+$')
	`, schemaName, relName)
	if err != nil {
		return ""
	}
	var leftovers []string
	for rows.Next() {
		var ns, idx string
		if rows.Scan(&ns, &idx) == nil {
			leftovers = append(leftovers, ns+"."+idx)
		}
	}
	rows.Close()

	var dropped []string
	for _, full := range leftovers {
		if _, e := db.conn.Exec(ctx, fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s", sanitizeTableName(full))); e == nil {
			dropped = append(dropped, full)
		}
	}
	return strings.Join(dropped, ", ")
}
