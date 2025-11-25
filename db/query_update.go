package db

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"qwash/sql"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RunQwashUpdate is the UPDATE-based version (like pgcompacttable)
// This version doesn't need transactions and should achieve 0% bloat
func (db *DB) RunQwashUpdate(tableName string, pageCount int) error {
	if pageCount <= 0 {
		return fmt.Errorf("invalid pageCount: must be > 0")
	}

	// Load query templates
	queries, err := sql.NewDebloatQueries()
	if err != nil {
		return fmt.Errorf("failed to load query templates: %w", err)
	}

	ctx := context.Background()

	// Step 1: Get the top `pageCount` pages
	var query bytes.Buffer
	err = queries.SelectHighestPages.Execute(&query, map[string]interface{}{
		"TableName": tableName,
		"PageCount": pageCount,
	})
	if err != nil {
		return fmt.Errorf("failed to render select_highest_pages template: %w", err)
	}

	rows, err := db.conn.Query(ctx, query.String())
	if err != nil {
		return fmt.Errorf("failed to fetch pages: %w", err)
	}
	defer rows.Close()

	var pages []int
	for rows.Next() {
		var page int
		if err := rows.Scan(&page); err != nil {
			return fmt.Errorf("failed to scan page: %w", err)
		}
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(pages) < pageCount {
		return fmt.Errorf("not enough pages to perform qwash: needed %d, got %d", pageCount, len(pages))
	}

	// Step 2: Get a sample column name for UPDATE (any column works)
	// We'll update that column to itself, forcing tuple relocation
	columnQuery := fmt.Sprintf(`
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = '%s'
		  AND column_name != 'tableoid'
		  AND column_name != 'cmax'
		  AND column_name != 'xmax'
		  AND column_name != 'cmin'
		  AND column_name != 'xmin'
		  AND column_name != 'ctid'
		LIMIT 1`, tableName)

	var updateColumn string
	err = db.conn.QueryRow(ctx, columnQuery).Scan(&updateColumn)
	if err != nil {
		return fmt.Errorf("failed to find column for UPDATE: %w", err)
	}

	// Build IN clause for pages
	placeholders := make([]string, pageCount)
	args := make([]interface{}, pageCount)
	for i, page := range pages {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = page
	}
	inClause := strings.Join(placeholders, ", ")

	// Step 3: UPDATE to force tuple relocation (like pgcompacttable)
	// UPDATE table SET col = col WHERE page IN (high_pages)
	// PostgreSQL will relocate tuples to first available space (filling holes)
	updateQuery := fmt.Sprintf(`
		UPDATE %s
		SET %s = %s
		WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (%s)`,
		pgx.Identifier{tableName}.Sanitize(),
		pgx.Identifier{updateColumn}.Sanitize(),
		pgx.Identifier{updateColumn}.Sanitize(),
		inClause)

	result, err := db.conn.Exec(ctx, updateQuery, args...)
	if err != nil {
		return fmt.Errorf("failed to UPDATE tuples: %w", err)
	}

	rowsAffected := result.RowsAffected()
	fmt.Printf("✅ Updated %d tuples from %d pages\n", rowsAffected, pageCount)

	return nil
}

// CompactTableUpdate uses the UPDATE-based approach
func (db *DB) CompactTableUpdate(tableName string, bloatPages int) error {
	if bloatPages <= 0 {
		return fmt.Errorf("invalid bloatPages: must be > 0")
	}

	ctx := context.Background()

	// Configuration
	const pagesPerRound = 5
	const vacuumEveryNPages = 250

	iterations := int(math.Ceil(float64(bloatPages) / float64(pagesPerRound)))
	pagesProcessed := 0

	fmt.Printf("\n🔁 %d iterations needed (processing %d pages/round)\n", iterations, pagesPerRound)
	fmt.Printf("🚀 Starting compaction for table '%s' (UPDATE-based)\n", tableName)
	fmt.Println("📊 Running initial VACUUM ANALYZE...")

	_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
	if err != nil {
		return fmt.Errorf("initial VACUUM ANALYZE failed: %w", err)
	}
	fmt.Println("✅ Initial VACUUM ANALYZE complete.")

	for i := 0; i < iterations; i++ {
		if err := db.RunQwashUpdate(tableName, pagesPerRound); err != nil {
			return fmt.Errorf("RunQwashUpdate failed at iteration %d: %w", i+1, err)
		}

		// VACUUM after EVERY round to clean up old tuple versions
		// UPDATE creates new versions, old ones must be cleaned immediately
		_, err := db.conn.Exec(ctx, fmt.Sprintf("VACUUM %s;", pgx.Identifier{tableName}.Sanitize()))
		if err != nil {
			return fmt.Errorf("VACUUM failed at iteration %d: %w", i+1, err)
		}

		pagesProcessed += pagesPerRound

		// ANALYZE every N pages
		if pagesProcessed >= vacuumEveryNPages || i == iterations-1 {
			_, err = db.conn.Exec(ctx, fmt.Sprintf("ANALYZE %s;", pgx.Identifier{tableName}.Sanitize()))
			if err != nil {
				return fmt.Errorf("ANALYZE failed at iteration %d: %w", i+1, err)
			}

			pagesProcessed = 0
		}
	}

	// Final verification
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
