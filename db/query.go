package db

import (
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

// Core of Qwash
// Core of Qwash
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

	// Step 1: Get the top `pageCount` ctids (based on tuple ID integer)
	query := fmt.Sprintf(`
		SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
		FROM %s
		GROUP BY page
		ORDER BY page DESC
		LIMIT %d`, tableName, pageCount)

	rows, err := tx.Query(ctx, query)
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

	// fmt.Printf("📦 Selected pages: %v\n", ctids)

	// Build placeholder string like ($1, $2, ..., $N)
	placeholders := make([]string, pageCount)
	args := make([]interface{}, pageCount)
	for i, ctid := range ctids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = ctid
	}
	inClause := strings.Join(placeholders, ", ")

	// Step 2: Create a temporary table
	tempTableSQL := fmt.Sprintf(`
		CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
		SELECT * FROM %[1]s
		WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (%s)`, tableName, inClause)
	if _, err := tx.Exec(ctx, tempTableSQL, args...); err != nil {
		return fmt.Errorf("failed to create temporary table: %w", err)
	}

	// Step 3: Delete from original table
	deleteSQL := fmt.Sprintf(`
		DELETE FROM %[1]s
		WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (%s)`, tableName, inClause)
	if _, err := tx.Exec(ctx, deleteSQL, args...); err != nil {
		return fmt.Errorf("failed to delete rows: %w", err)
	}

	// Step 4: Reinsert from temp
	insertSQL := fmt.Sprintf(`INSERT INTO %[1]s SELECT * FROM qwash`, tableName)
	if _, err := tx.Exec(ctx, insertSQL); err != nil {
		return fmt.Errorf("failed to insert rows back: %w", err)
	}

	return nil
}

func (db *DB) CompactTable(tableName string, bloatPages int) error {
	if bloatPages <= 0 {
		return fmt.Errorf("invalid targetPages: must be > 0")
	}

	// pageCount :=

	iterations := int(math.Ceil(float64(bloatPages) / 2.0))
	// iterations := int(math.Ceil(float64(bloatPages) / 4.0))
	// iterations := int(math.Ceil(float64(bloatPages) / 16.0))

	ctx := context.Background()

	fmt.Printf("\n🔁 %d iterations needed \n", iterations)
	fmt.Printf("🚀 Starting compaction for table '%s'\n", tableName)
	fmt.Println("📊 Running initial VACUUM ANALYZE...")

	_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
	if err != nil {
		return fmt.Errorf("initial VACUUM ANALYZE failed: %w", err)
	}
	fmt.Println("✅ Initial VACUUM ANALYZE complete.")

	for i := 0; i < iterations; i++ {
		//fmt.Printf("\n🔁 Qwash iteration %d/%d\n", i+1, iterations)

		if err := db.RunQwash(tableName, 2); err != nil {
			return fmt.Errorf("RunQwash failed at iteration %d: %w", i+1, err)
		}
		//fmt.Printf("✅ RunQwash round %d complete.\n", i+1)

		//fmt.Println("📊 Running VACUUM...")
		_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
		if err != nil {
			return fmt.Errorf("VACUUM failed at iteration %d: %w", i+1, err)
		}
		//fmt.Printf("✅ VACUUM round %d complete.\n", i+1)

		//fmt.Println("📊 Running ANALYZE...")
		_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
		if err != nil {
			return fmt.Errorf("ANALYZE failed at iteration %d: %w", i+1, err)
		}
		//fmt.Printf("✅ ANALYZE round %d complete.\n", i+1)
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
