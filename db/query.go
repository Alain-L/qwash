package db

import (
	"bytes"
	"context"
	"fmt"
	"math"
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

	// Load query templates
	queries, err := sql.NewDebloatQueries()
	if err != nil {
		return fmt.Errorf("failed to load query templates: %w", err)
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

	// Step 1: Get the top `pageCount` pages
	var query bytes.Buffer
	err = queries.SelectHighestPages.Execute(&query, map[string]interface{}{
		"TableName": tableName,
		"PageCount": pageCount,
	})
	if err != nil {
		return fmt.Errorf("failed to render select_highest_pages template: %w", err)
	}

	rows, err := tx.Query(ctx, query.String())
	if err != nil {
		return fmt.Errorf("failed to fetch ctids: %w", err)
	}
	defer rows.Close()

	var ctids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan ctid: %w", err)
		}
		ctids = append(ctids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ctids) < pageCount {
		return fmt.Errorf("not enough rows to perform qwash: needed %d, got %d", pageCount, len(ctids))
	}

	// Build placeholder string like ($1, $2, ..., $N)
	placeholders := make([]string, pageCount)
	args := make([]interface{}, pageCount)
	for i, ctid := range ctids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = ctid
	}
	inClause := strings.Join(placeholders, ", ")

	// Step 2: Create a temporary table
	query.Reset()
	err = queries.CreateTempTable.Execute(&query, map[string]interface{}{
		"TableName": tableName,
		"InClause":  inClause,
	})
	if err != nil {
		return fmt.Errorf("failed to render create_temp_table template: %w", err)
	}

	if _, err := tx.Exec(ctx, query.String(), args...); err != nil {
		return fmt.Errorf("failed to create temporary table: %w", err)
	}

	// Step 3: Delete from original table
	query.Reset()
	err = queries.DeleteFromTable.Execute(&query, map[string]interface{}{
		"TableName": tableName,
		"InClause":  inClause,
	})
	if err != nil {
		return fmt.Errorf("failed to render delete_from_table template: %w", err)
	}

	if _, err := tx.Exec(ctx, query.String(), args...); err != nil {
		return fmt.Errorf("failed to delete rows: %w", err)
	}

	// Step 4: Reinsert from temp
	query.Reset()
	err = queries.ReinsertFromTemp.Execute(&query, map[string]interface{}{
		"TableName": tableName,
	})
	if err != nil {
		return fmt.Errorf("failed to render reinsert_from_temp template: %w", err)
	}

	if _, err := tx.Exec(ctx, query.String()); err != nil {
		return fmt.Errorf("failed to insert rows back: %w", err)
	}

	return nil
}

func (db *DB) CompactTable(tableName string, bloatPages int) error {
	if bloatPages <= 0 {
		return fmt.Errorf("invalid targetPages: must be > 0")
	}

	ctx := context.Background()

	// Configuration aligned with pgcompacttable for optimal performance
	const pagesPerRound = 5          // Process 5 pages per iteration (vs 2 previously)
	const vacuumEveryNPages = 250    // VACUUM every 250 pages (vs every round)

	iterations := int(math.Ceil(float64(bloatPages) / float64(pagesPerRound)))
	pagesProcessed := 0

	fmt.Printf("\n🔁 %d iterations needed (processing %d pages/round)\n", iterations, pagesPerRound)
	fmt.Printf("🚀 Starting compaction for table '%s'\n", tableName)
	fmt.Println("📊 Running initial VACUUM ANALYZE...")

	_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
	if err != nil {
		return fmt.Errorf("initial VACUUM ANALYZE failed: %w", err)
	}
	fmt.Println("✅ Initial VACUUM ANALYZE complete.")

	for i := 0; i < iterations; i++ {
		if err := db.RunQwash(tableName, pagesPerRound); err != nil {
			return fmt.Errorf("RunQwash failed at iteration %d: %w", i+1, err)
		}

		pagesProcessed += pagesPerRound

		// VACUUM/ANALYZE only every N pages (like pgcompacttable)
		if pagesProcessed >= vacuumEveryNPages || i == iterations-1 {
			_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
			if err != nil {
				return fmt.Errorf("VACUUM failed at iteration %d: %w", i+1, err)
			}

			_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
			if err != nil {
				return fmt.Errorf("ANALYZE failed at iteration %d: %w", i+1, err)
			}

			pagesProcessed = 0 // Reset counter
		}
	}

	// 🔍 Affichage des deux plus grands ctids à la fin
	query := fmt.Sprintf(`SELECT ctid FROM %s ORDER BY ctid DESC LIMIT 2`, pgx.Identifier{tableName}.Sanitize())
	rows, err := db.conn.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to fetch ctids after compaction: %w", err)
	}
	defer rows.Close()

	fmt.Println("\n🔎 Highest CTIDs after final VACUUM ANALYZE:")
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

	fmt.Println("🎯 Compaction process complete.")
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
		_           string // table_name
		_           int    // live_tup
		_           int64  // dead_tup
		minPages    int
		actualPages int
		_           int     // fillfactor
		_           string  // relation_size
		_           string  // TOAST_size
		_           string  // bloat_size
		_           float64 // bloat_pct
	)

	err := row.Scan(
		new(string), // table_name
		new(int),    // live_tup
		new(int64),  // dead_tup
		&minPages,
		&actualPages,
		new(int),     // fillfactor
		new(string),  // relation_size
		new(string),  // TOAST_size
		new(string),  // bloat_size
		new(float64), // bloat_pct
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
