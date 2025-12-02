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

// hasIdentityColumns checks if table has any IDENTITY columns (GENERATED ALWAYS AS IDENTITY).
// This is required to determine whether to use OVERRIDING SYSTEM VALUE in INSERT statements.
func (db *DB) hasIdentityColumns(tableName string) (bool, error) {
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

	query := `
		SELECT COUNT(*) > 0
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND a.attidentity IN ('a', 'd')
		  AND NOT a.attisdropped
	`

	var hasIdentity bool
	err := db.QueryRow(ctx, query, schemaName, relName).Scan(&hasIdentity)
	return hasIdentity, err
}

// printProgress prints a single-line progress that updates in place
func printProgress(db *DB, tableName string, pass int) {
	if db.SilentProgress {
		return
	}
	tableWord := "tables"
	if db.TotalTables == 1 {
		tableWord = "table"
	}
	displayName := tableName
	if len(displayName) > 40 {
		displayName = displayName[:37] + "..."
	}
	fmt.Printf("\r\033[K%-40s | Pass %2d | %2d/%2d %s",
		displayName, pass, db.CurrentTableIndex, db.TotalTables, tableWord)
}

// clearProgress clears the progress line and moves to next line
func clearProgress(db *DB) {
	if db.SilentProgress {
		return
	}
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
	// Check for IDENTITY columns to use OVERRIDING SYSTEM VALUE if needed
	hasIdentity, err := db.hasIdentityColumns(tableName)
	if err != nil {
		return fmt.Errorf("failed to check identity columns: %w", err)
	}

	var reinsertQuery string
	if hasIdentity {
		reinsertQuery = fmt.Sprintf(`
			INSERT INTO %s OVERRIDING SYSTEM VALUE
			SELECT * FROM qwash_tmp
		`, sanitizeTableName(tableName))
	} else {
		reinsertQuery = fmt.Sprintf(`
			INSERT INTO %s SELECT * FROM qwash_tmp
		`, sanitizeTableName(tableName))
	}

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
	// Check for IDENTITY columns to use OVERRIDING SYSTEM VALUE if needed
	hasIdentity, err := db.hasIdentityColumns(tableName)
	if err != nil {
		return fmt.Errorf("failed to check identity columns: %w", err)
	}

	var reinsertQuery string
	if hasIdentity {
		reinsertQuery = fmt.Sprintf(`
			INSERT INTO %s OVERRIDING SYSTEM VALUE
			SELECT * FROM qwash_tmp_filled
		`, sanitizeTableName(tableName))
	} else {
		reinsertQuery = fmt.Sprintf(`
			INSERT INTO %s SELECT * FROM qwash_tmp_filled
		`, sanitizeTableName(tableName))
	}

	if _, err := tx.Exec(ctx, reinsertQuery); err != nil {
		return fmt.Errorf("failed to reinsert rows: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
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

// RunQwashUpdate uses UPDATE technique (like pgcompacttable) to move tuples.
// This is simpler than DELETE/INSERT and avoids issues with sequences/IDENTITY.
// The UPDATE SET col = col forces PostgreSQL to create new tuple versions
// in lower pages (via FSM), leaving high pages empty for VACUUM truncation.
func (db *DB) RunQwashUpdate(tableName string, pageCount int) error {
	if pageCount <= 0 {
		return fmt.Errorf("invalid pageCount: must be > 0")
	}

	ctx := context.Background()

	// Find an updatable column
	column, err := db.getUpdatableColumn(tableName)
	if err != nil {
		return err
	}

	// Get max page BEFORE update
	var maxPageBefore int
	pageQuery := fmt.Sprintf(`SELECT MAX((ctid::text::point)[0]::bigint) FROM %s`, sanitizeTableName(tableName))
	db.QueryRow(ctx, pageQuery).Scan(&maxPageBefore)

	// Use stored procedure like pgcompacttable does
	// This ensures exact same execution pattern
	procName := fmt.Sprintf("qwash_update_%d", time.Now().UnixNano())

	createProc := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s(i_ctids tid[])
		RETURNS SETOF tid AS $$
		BEGIN
			RETURN QUERY
			UPDATE ONLY %s
			SET %s = %s
			WHERE ctid = ANY(i_ctids)
			RETURNING ctid;
		END;
		$$ LANGUAGE plpgsql
	`, procName, sanitizeTableName(tableName),
	   pgx.Identifier{column}.Sanitize(), pgx.Identifier{column}.Sanitize())

	_, err = db.conn.Exec(ctx, createProc)
	if err != nil {
		return fmt.Errorf("failed to create procedure: %w", err)
	}
	defer db.conn.Exec(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s", procName))

	// Fetch CTIDs from ONE page at a time (like pgcompacttable: pages/round = 1)
	ctidQuery := fmt.Sprintf(`
		SELECT array_agg(ctid) FROM %s
		WHERE (ctid::text::point)[0]::bigint = (
			SELECT MAX((ctid::text::point)[0]::bigint) FROM %s
		)
	`, sanitizeTableName(tableName), sanitizeTableName(tableName))

	var ctidArray []string
	err = db.QueryRow(ctx, ctidQuery).Scan(&ctidArray)
	if err != nil || len(ctidArray) == 0 {
		return nil
	}

	// Call procedure with CTIDs (exactly like pgcompacttable)
	callQuery := fmt.Sprintf(`SELECT * FROM %s($1::tid[])`, procName)
	rows, err := db.conn.Query(ctx, callQuery, ctidArray)
	if err != nil {
		return fmt.Errorf("UPDATE via procedure failed: %w", err)
	}

	var updatedCount int
	for rows.Next() {
		updatedCount++
	}
	rows.Close()

	// Check max page AFTER update
	var maxPageAfter int
	db.QueryRow(ctx, pageQuery).Scan(&maxPageAfter)

	if db.Verbose {
		fmt.Printf("    [UPDATE] col=%s, %d rows, max_page: %d → %d\n",
			column, updatedCount, maxPageBefore, maxPageAfter)
	}

	return nil
}

// RunQwashUpdateFilledPages uses UPDATE to empty the least filled pages.
// Similar to RunQwashFilledPages but uses UPDATE instead of DELETE/INSERT.
func (db *DB) RunQwashUpdateFilledPages(tableName string, pageCount int) error {
	if pageCount <= 0 {
		return fmt.Errorf("invalid pageCount: must be > 0")
	}

	ctx := context.Background()

	// Find an updatable column
	column, err := db.getUpdatableColumn(tableName)
	if err != nil {
		return err
	}

	// Select CTIDs from the LEAST filled pages (by tuple count)
	updateQuery := fmt.Sprintf(`
		UPDATE ONLY %s
		SET %s = %s
		WHERE ctid = ANY(
			ARRAY(
				SELECT ctid FROM %s
				WHERE (ctid::text::point)[0]::bigint IN (
					SELECT (ctid::text::point)[0]::bigint
					FROM %s
					GROUP BY (ctid::text::point)[0]::bigint
					ORDER BY COUNT(*) ASC
					LIMIT %d
				)
			)
		)
	`,
		sanitizeTableName(tableName),
		pgx.Identifier{column}.Sanitize(),
		pgx.Identifier{column}.Sanitize(),
		sanitizeTableName(tableName),
		sanitizeTableName(tableName),
		pageCount,
	)

	_, err = db.conn.Exec(ctx, updateQuery)
	if err != nil {
		return fmt.Errorf("UPDATE failed: %w", err)
	}

	return nil
}

// CompactTableUpdate uses the UPDATE SET col=col method to compact tables.
// It uses bloat estimation to determine the target page count,
// processing only the estimated bloated pages instead of the full table.
// This approach is efficient and may require 1-2 passes for complete compaction.
func (db *DB) CompactTableUpdate(tableName string) error {
	ctx := context.Background()

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

	// Find an updatable column (like pgcompacttable: prefer non-indexed, fixed-length)
	column, err := db.getUpdatableColumn(tableName)
	if err != nil {
		return err
	}

	// Set session_replication_role = replica to disable triggers (like pgcompacttable)
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

	// Create stored procedure (exactly like pgcompacttable)
	// Key insight: pgcompacttable loops internally, re-updating tuples until they move!
	// This fills the page with dead tuples until HOT is no longer possible.
	// Use WorkerID + timestamp to ensure unique procedure name in parallel mode
	procName := fmt.Sprintf("qwash_compact_w%d_%d", db.WorkerID, time.Now().UnixNano())
	createProc := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s(
			i_table_ident text,
			i_column_ident text,
			i_to_page integer,
			i_page_offset integer,
			i_max_tuples_per_page integer
		) RETURNS integer AS $$
		DECLARE
			_from_page integer := i_to_page - i_page_offset + 1;
			_min_ctid tid;
			_max_ctid tid;
			_ctid_list tid[];
			_next_ctid_list tid[];
			_ctid tid;
			_loop integer;
			_result_page integer;
			_update_query text :=
				'UPDATE ONLY ' || i_table_ident ||
				' SET ' || i_column_ident || ' = ' || i_column_ident ||
				' WHERE ctid = ANY($1) RETURNING ctid';
		BEGIN
			-- Define minimal and maximal ctid values of the range
			_min_ctid := (_from_page, 1)::text::tid;
			_max_ctid := (i_to_page, i_max_tuples_per_page)::text::tid;

			-- Build a list of possible ctid values of the range
			SELECT array_agg((pi, ti)::text::tid)
			INTO _ctid_list
			FROM generate_series(_from_page, i_to_page) AS pi
			CROSS JOIN generate_series(1, i_max_tuples_per_page) AS ti;

			<<_outer_loop>>
			FOR _loop IN 1..i_max_tuples_per_page LOOP
				_next_ctid_list := array[]::tid[];

				-- Update all the tuples in the range
				FOR _ctid IN EXECUTE _update_query USING _ctid_list
				LOOP
					IF _ctid > _max_ctid THEN
						-- Tuple moved ABOVE the range (problem)
						_result_page := -1;
						EXIT _outer_loop;
					ELSIF _ctid >= _min_ctid THEN
						-- Tuple still in the range, needs more updates
						_next_ctid_list := _next_ctid_list || _ctid;
					END IF;
					-- If _ctid < _min_ctid, tuple moved to lower page (success!)
				END LOOP;

				_ctid_list := _next_ctid_list;

				-- Finish if all tuples have moved out of the range
				IF coalesce(array_length(_ctid_list, 1), 0) = 0 THEN
					_result_page := _from_page - 1;
					EXIT _outer_loop;
				END IF;
			END LOOP;

			IF _loop = i_max_tuples_per_page AND _result_page IS NULL THEN
				_result_page := -2; -- Max loops reached
			END IF;

			RETURN _result_page;
		END;
		$$ LANGUAGE plpgsql
	`, procName)

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

	// pgcompacttable parameters
	// pages_per_round = 1 (process one page at a time)
	// pages_before_vacuum = initial_pages / 16 (default ratio from pgcompacttable)
	// max_tuples_per_page = ~226 for 8KB pages (used for loop limit)
	pagesPerRound := 1
	pagesBeforeVacuum := initialPages / 16
	if pagesBeforeVacuum < 1 {
		pagesBeforeVacuum = 1
	}
	maxTuplesPerPage := 226 // Conservative estimate for 8KB pages

	// Prepare table and column identifiers for the procedure
	tableIdent := sanitizeTableName(tableName)
	columnIdent := pgx.Identifier{column}.Sanitize()

	// Main loop (exactly like pgcompacttable)
	pagesProcessed := 0
	pagesSinceVacuum := 0
	currentPage := initialPages - 1 // Start from the last page (0-indexed)

	for currentPage > toPage {
		// Call the procedure to clean pages from currentPage down by pagesPerRound
		var resultPage int
		err = db.QueryRow(ctx,
			fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5)", procName),
			tableIdent, columnIdent, currentPage, pagesPerRound, maxTuplesPerPage,
		).Scan(&resultPage)

		if err != nil {
			return fmt.Errorf("procedure call failed at page %d: %w", currentPage, err)
		}

		if resultPage == -1 {
			// Tuples moved to higher pages (shouldn't happen normally)
			if db.Verbose {
				fmt.Printf("\n  Warning: tuples moved to higher page at page %d\n", currentPage)
			}
		} else if resultPage == -2 {
			// Max loops reached without moving all tuples
			if db.Verbose {
				fmt.Printf("\n  Warning: max loops reached at page %d\n", currentPage)
			}
		}

		pagesProcessed += pagesPerRound
		pagesSinceVacuum += pagesPerRound
		currentPage -= pagesPerRound

		// Progress display
		if !db.SilentProgress {
			fmt.Printf("\r\033[K  Page %d/%d | vacuum in %d",
				currentPage, toPage, pagesBeforeVacuum-pagesSinceVacuum)
		}

		// VACUUM every N pages (like pgcompacttable)
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

func (db *DB) CompactTable(tableName string, initialBloatPages int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	// Acquire advisory lock to prevent concurrent compaction
	locked, err := db.acquireTableLock(tableName)
	if err != nil {
		return fmt.Errorf("failed to check table lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("table '%s' is already being compacted by another qwash process", tableName)
	}

	// Ensure lock is released even if function panics or returns early
	defer func() {
		if unlockErr := db.releaseTableLock(tableName); unlockErr != nil {
			if db.Verbose {
				fmt.Printf("  Warning: failed to release lock for %s: %v\n", tableName, unlockErr)
			}
		}
	}()

	ctx := context.Background()

	// Set session_replication_role = replica to disable triggers during compaction
	_, err = db.conn.Exec(ctx, "SET session_replication_role = replica")
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

	if db.Verbose {
		fmt.Printf("Compacting '%s' (%d bloat pages)...\n", tableName, initialBloatPages)
	}

	// VACUUM once at the beginning to establish clean baseline
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
			clearProgress(db)
			return fmt.Errorf("ANALYZE failed at pass %d: %w", passNumber, err)
		}

		// Recalculate bloat and get actual pages
		bloatPages, err := db.GetBloatPages(tableName)
		if err != nil {
			clearProgress(db)
			return fmt.Errorf("failed to calculate bloat at pass %d: %w", passNumber, err)
		}

		// Get actual page count from pg_class for accurate tracking
		actualPages, err := db.GetTablePages(tableName)
		if err != nil {
			clearProgress(db)
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
						printProgress(db, tableName, passNumber)

						unblockErr := db.RunQwashFilledPages(tableName, chunkSize)
						if unblockErr != nil {
							clearProgress(db)
							return fmt.Errorf("unblock operation failed: %w", unblockErr)
						}
						pagesLeft -= chunkSize

						// VACUUM after each chunk
						_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
						if err != nil {
							clearProgress(db)
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
			printProgress(db, tableName, passNumber)

			// Execute qwash on pagesThisRound pages
			qwashErr := db.RunQwash(tableName, pagesThisRound)
			if qwashErr != nil {
				clearProgress(db)
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, qwashErr)
			}

			pagesRemaining -= pagesThisRound
			roundNumber++
			totalRounds++

			// VACUUM after each round to reclaim freed pages
			_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
			if err != nil {
				clearProgress(db)
				return fmt.Errorf("VACUUM failed at pass %d, round %d: %w", passNumber, roundNumber, err)
			}
		}

		clearProgress(db)
		if db.Verbose {
			fmt.Printf("  Pass %d: %d rounds, %d pages\n", passNumber, roundNumber, actualPages)
		}

		// Run ANALYZE after each pass to update statistics for next bloat check
		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Reset sequences after debloat (best practice to avoid duplicate key errors on next INSERT)
	if err := db.resetSequences(tableName); err != nil {
		// Non-fatal: log but continue
		if db.Verbose {
			fmt.Printf("  Warning: failed to reset sequences: %v\n", err)
		}
	}

	// Final VACUUM to truncate empty trailing pages
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	// Get final page count
	finalPages, _ := db.GetTablePages(tableName)
	if db.Verbose {
		fmt.Printf("  Done: %d -> %d pages (%d passes, %d rounds)\n",
			initialActualPages, finalPages, passNumber, totalRounds)
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

// resetSequences resets all sequences owned by table columns to MAX(column_value).
// This is a best practice after debloat operations using INSERT...SELECT with explicit IDs,
// which don't trigger nextval() and leave sequences out of sync.
// Handles both SERIAL (sequence) and IDENTITY columns.
func (db *DB) resetSequences(tableName string) error {
	ctx := context.Background()

	// Handle schema-qualified table names
	var schemaName, relName string
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName = parts[0]
		relName = parts[1]
	} else {
		schemaName = "public"
		relName = tableName
	}

	// Query to find all sequences owned by the table's columns
	query := `
		SELECT
			a.attname AS column_name,
			s.relname AS sequence_name,
			ns.nspname AS sequence_schema
		FROM pg_class c
		JOIN pg_namespace n ON c.relnamespace = n.oid
		JOIN pg_attribute a ON a.attrelid = c.oid
		JOIN pg_depend d ON d.refobjid = c.oid AND d.refobjsubid = a.attnum
		JOIN pg_class s ON s.oid = d.objid AND s.relkind = 'S'
		JOIN pg_namespace ns ON s.relnamespace = ns.oid
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND a.attnum > 0
		  AND NOT a.attisdropped
	`

	rows, err := db.conn.Query(ctx, query, schemaName, relName)
	if err != nil {
		// Non-fatal: table might not have sequences
		return nil
	}
	defer rows.Close()

	sequenceCount := 0
	for rows.Next() {
		var columnName, sequenceName, sequenceSchema string
		if err := rows.Scan(&columnName, &sequenceName, &sequenceSchema); err != nil {
			continue // Skip problematic sequences
		}

		// Get max value for this column
		maxQuery := fmt.Sprintf("SELECT COALESCE(MAX(%s), 1) FROM %s",
			pgx.Identifier{columnName}.Sanitize(),
			sanitizeTableName(tableName))

		var maxValue int64
		err := db.conn.QueryRow(ctx, maxQuery).Scan(&maxValue)
		if err != nil {
			continue // Skip if we can't get max value
		}

		// Reset the sequence
		seqName := fmt.Sprintf("%s.%s", sequenceSchema, sequenceName)
		setvalQuery := fmt.Sprintf("SELECT setval('%s', %d)", seqName, maxValue)

		_, err = db.conn.Exec(ctx, setvalQuery)
		if err != nil {
			continue // Non-fatal: skip if setval fails
		}

		sequenceCount++
	}

	// Log if verbose and sequences were reset
	if sequenceCount > 0 && db.Verbose {
		fmt.Printf("  Reset %d sequence(s) for table %s\n", sequenceCount, tableName)
	}

	return nil
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

// CompactTableFast performs faster compaction with adaptive vacuum threshold (~99% efficiency).
// This is faster than CompactTable but may leave 1-2 pages of bloat on very large tables.
func (db *DB) CompactTableFast(tableName string, initialBloatPages int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	// Acquire advisory lock to prevent concurrent compaction
	locked, err := db.acquireTableLock(tableName)
	if err != nil {
		return fmt.Errorf("failed to check table lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("table '%s' is already being compacted by another qwash process", tableName)
	}

	// Ensure lock is released even if function panics or returns early
	defer func() {
		if unlockErr := db.releaseTableLock(tableName); unlockErr != nil {
			if db.Verbose {
				fmt.Printf("  Warning: failed to release lock for %s: %v\n", tableName, unlockErr)
			}
		}
	}()

	ctx := context.Background()

	// Set session_replication_role = replica to disable triggers during compaction
	_, err = db.conn.Exec(ctx, "SET session_replication_role = replica")
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

	if db.Verbose {
		fmt.Printf("FAST compacting '%s' (%d bloat pages, target <%.0f%%)...\n", tableName, initialBloatPages, targetBloatPct)
	}

	// VACUUM once at the beginning
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
						printProgress(db, tableName, passNumber)

						unblockErr := db.RunQwashFilledPages(tableName, chunkSize)
						if unblockErr != nil {
							return fmt.Errorf("unblock operation failed: %w", unblockErr)
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
			printProgress(db, tableName, passNumber)

			// Execute qwash
			qwashErr := db.RunQwash(tableName, pagesThisRound)
			if qwashErr != nil {
				clearProgress(db)
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, qwashErr)
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
					clearProgress(db)
					return fmt.Errorf("VACUUM failed: %w", err)
				}
				roundsSinceLastVacuum = 0
			}
		}

		clearProgress(db)
		if db.Verbose {
			fmt.Printf("  Pass %d: %d rounds, %d pages\n", passNumber, roundNumber, actualPages)
		}

		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Reset sequences after debloat (best practice to avoid duplicate key errors on next INSERT)
	if err := db.resetSequences(tableName); err != nil {
		// Non-fatal: log but continue
		if db.Verbose {
			fmt.Printf("  Warning: failed to reset sequences: %v\n", err)
		}
	}

	// Final VACUUM
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	finalPages, _ := db.GetTablePages(tableName)
	if db.Verbose {
		fmt.Printf("  Done: %d -> %d pages (%d passes, %d rounds)\n",
			initialActualPages, finalPages, passNumber, totalRounds)
	}
	return nil
}

// CompactTableSlow performs conservative compaction, processing 1 page at a time with delays.
// This approach is similar to pgcompacttable: very gentle on production systems.
func (db *DB) CompactTableSlow(tableName string, initialBloatPages int, delayMs int) error {
	if initialBloatPages <= 0 {
		return fmt.Errorf("invalid bloat pages: must be > 0")
	}

	// Acquire advisory lock to prevent concurrent compaction
	locked, err := db.acquireTableLock(tableName)
	if err != nil {
		return fmt.Errorf("failed to check table lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("table '%s' is already being compacted by another qwash process", tableName)
	}

	// Ensure lock is released even if function panics or returns early
	defer func() {
		if unlockErr := db.releaseTableLock(tableName); unlockErr != nil {
			if db.Verbose {
				fmt.Printf("  Warning: failed to release lock for %s: %v\n", tableName, unlockErr)
			}
		}
	}()

	ctx := context.Background()
	delay := time.Duration(delayMs) * time.Millisecond

	// Set session_replication_role = replica to disable triggers during compaction
	_, err = db.conn.Exec(ctx, "SET session_replication_role = replica")
	if err != nil {
		return fmt.Errorf("failed to set session_replication_role: %w", err)
	}
	defer db.conn.Exec(ctx, "SET session_replication_role = DEFAULT")

	// Get initial actual page count
	initialActualPages, err := db.GetTablePages(tableName)
	if err != nil {
		return fmt.Errorf("failed to get initial page count: %w", err)
	}

	if db.Verbose {
		fmt.Printf("SLOW compacting '%s' (%d bloat pages, %dms delay)...\n", tableName, initialBloatPages, delayMs)
	}

	// VACUUM once at the beginning to establish clean baseline
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
			clearProgress(db)
			return fmt.Errorf("ANALYZE failed at pass %d: %w", passNumber, err)
		}

		// Recalculate bloat
		bloatPages, err := db.GetBloatPages(tableName)
		if err != nil {
			clearProgress(db)
			return fmt.Errorf("failed to calculate bloat at pass %d: %w", passNumber, err)
		}

		actualPages, err := db.GetTablePages(tableName)
		if err != nil {
			clearProgress(db)
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
						printProgress(db, tableName, passNumber)

						unblockErr := db.RunQwashFilledPages(tableName, 1)
						if unblockErr != nil {
							clearProgress(db)
							return fmt.Errorf("unblock operation failed: %w", unblockErr)
						}
						pagesLeft--

						// VACUUM after each page
						_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
						if err != nil {
							clearProgress(db)
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
			printProgress(db, tableName, passNumber)

			// Execute qwash on 1 page
			qwashErr := db.RunQwash(tableName, pagesThisRound)
			if qwashErr != nil {
				clearProgress(db)
				return fmt.Errorf("RunQwash failed at pass %d, round %d: %w", passNumber, roundNumber+1, qwashErr)
			}

			pagesRemaining -= pagesThisRound
			roundNumber++
			totalRounds++

			// VACUUM after each page
			_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
			if err != nil {
				clearProgress(db)
				return fmt.Errorf("VACUUM failed at pass %d, round %d: %w", passNumber, roundNumber, err)
			}

			// Delay between operations (like pgcompacttable)
			time.Sleep(delay)
		}

		clearProgress(db)
		if db.Verbose {
			fmt.Printf("  Pass %d: %d rounds, %d pages\n", passNumber, roundNumber, actualPages)
		}

		// Run ANALYZE after each pass
		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", sanitizeTableName(tableName)))
		if err != nil {
			return fmt.Errorf("ANALYZE failed after pass %d: %w", passNumber, err)
		}
	}

	// Reset sequences after debloat (best practice to avoid duplicate key errors on next INSERT)
	if err := db.resetSequences(tableName); err != nil {
		// Non-fatal: log but continue
		if db.Verbose {
			fmt.Printf("  Warning: failed to reset sequences: %v\n", err)
		}
	}

	// Final VACUUM
	_, err = db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", sanitizeTableName(tableName)))
	if err != nil {
		return fmt.Errorf("final VACUUM failed: %w", err)
	}

	finalPages, _ := db.GetTablePages(tableName)
	if db.Verbose {
		fmt.Printf("  Done: %d -> %d pages (%d passes, %d rounds)\n",
			initialActualPages, finalPages, passNumber, totalRounds)
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

