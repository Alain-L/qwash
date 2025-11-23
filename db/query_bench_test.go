package db

import (
	"bytes"
	"fmt"
	"qwash/sql"
	"strings"
	"testing"
)

// BenchmarkQueryGeneration compares hardcoded vs templated query generation
func BenchmarkQueryGeneration(b *testing.B) {
	tableName := "test_table"
	pageCount := 5

	b.Run("Hardcoded", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = fmt.Sprintf(`
				SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
				FROM %s
				GROUP BY page
				ORDER BY page DESC
				LIMIT %d`, tableName, pageCount)
		}
	})

	b.Run("Templated", func(b *testing.B) {
		queries, err := sql.NewDebloatQueries()
		if err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var buf bytes.Buffer
			_ = queries.SelectHighestPages.Execute(&buf, map[string]interface{}{
				"TableName": tableName,
				"PageCount": pageCount,
			})
		}
	})

	b.Run("TemplatedWithParsing", func(b *testing.B) {
		// Simulates parsing templates on every call (worst case)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			queries, err := sql.NewDebloatQueries()
			if err != nil {
				b.Fatal(err)
			}
			var buf bytes.Buffer
			_ = queries.SelectHighestPages.Execute(&buf, map[string]interface{}{
				"TableName": tableName,
				"PageCount": pageCount,
			})
		}
	})
}

// BenchmarkFullQuerySequence tests the complete debloat query sequence
func BenchmarkFullQuerySequence(b *testing.B) {
	tableName := "test_table"
	pageCount := 5

	b.Run("Hardcoded", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// Step 1: Select highest pages
			_ = fmt.Sprintf(`
				SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
				FROM %s
				GROUP BY page
				ORDER BY page DESC
				LIMIT %d`, tableName, pageCount)

			// Build placeholders
			placeholders := make([]string, pageCount)
			for j := 0; j < pageCount; j++ {
				placeholders[j] = fmt.Sprintf("$%d", j+1)
			}
			inClause := strings.Join(placeholders, ", ")

			// Step 2: Create temp table
			_ = fmt.Sprintf(`
				CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
				SELECT * FROM %[1]s
				WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (%s)`, tableName, inClause)

			// Step 3: Delete
			_ = fmt.Sprintf(`
				DELETE FROM %[1]s
				WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (%s)`, tableName, inClause)

			// Step 4: Reinsert
			_ = fmt.Sprintf(`INSERT INTO %[1]s SELECT * FROM qwash`, tableName)
		}
	})

	b.Run("Templated", func(b *testing.B) {
		queries, err := sql.NewDebloatQueries()
		if err != nil {
			b.Fatal(err)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// Step 1: Select highest pages
			var buf bytes.Buffer
			_ = queries.SelectHighestPages.Execute(&buf, map[string]interface{}{
				"TableName": tableName,
				"PageCount": pageCount,
			})

			// Build placeholders
			placeholders := make([]string, pageCount)
			for j := 0; j < pageCount; j++ {
				placeholders[j] = fmt.Sprintf("$%d", j+1)
			}
			inClause := strings.Join(placeholders, ", ")

			// Step 2: Create temp table
			buf.Reset()
			_ = queries.CreateTempTable.Execute(&buf, map[string]interface{}{
				"TableName": tableName,
				"InClause":  inClause,
			})

			// Step 3: Delete
			buf.Reset()
			_ = queries.DeleteFromTable.Execute(&buf, map[string]interface{}{
				"TableName": tableName,
				"InClause":  inClause,
			})

			// Step 4: Reinsert
			buf.Reset()
			_ = queries.ReinsertFromTemp.Execute(&buf, map[string]interface{}{
				"TableName": tableName,
			})
		}
	})
}
