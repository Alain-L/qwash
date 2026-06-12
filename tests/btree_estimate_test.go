package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"qwash/analysis"
	"qwash/db"
	"qwash/output"
)

// =============================================================================
// B-TREE INDEX BLOAT ESTIMATION TESTS
// =============================================================================

// createBloatedIndexTable creates a table with B-Tree indexes and deterministic bloat.
// rows: total rows to insert
// deletePercent: percentage of rows to delete (e.g., 50 means delete 50%)
func createBloatedIndexTable(t *testing.T, conn *db.DB, tableName string, rows int, deletePercent int) {
	t.Helper()
	ctx := context.Background()

	_, err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100),
			value INTEGER
		) WITH (autovacuum_enabled = false)
	`, tableName))
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	_, err = conn.Exec(ctx, fmt.Sprintf(
		"CREATE INDEX idx_%s_data ON %s (data)", tableName, tableName))
	if err != nil {
		t.Fatalf("Failed to create index on data: %v", err)
	}

	_, err = conn.Exec(ctx, fmt.Sprintf(
		"CREATE INDEX idx_%s_value ON %s (value)", tableName, tableName))
	if err != nil {
		t.Fatalf("Failed to create index on value: %v", err)
	}

	_, err = conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data, value)
		SELECT 'row_' || g || '_padding_data_here', g %% 1000
		FROM generate_series(1, %d) g
	`, tableName, rows))
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	if deletePercent > 0 {
		_, err = conn.Exec(ctx, fmt.Sprintf(
			"DELETE FROM %s WHERE id %% 100 < %d", tableName, deletePercent))
		if err != nil {
			t.Fatalf("Failed to delete rows: %v", err)
		}
	}

	// VACUUM ANALYZE to update statistics (no REINDEX — keep index bloated)
	_, err = conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s", tableName))
	if err != nil {
		t.Fatalf("Failed to vacuum analyze: %v", err)
	}
}

// TestBtreeEstimateBasic tests B-Tree index bloat estimation with 50% delete
func TestBtreeEstimateBasic(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "btree_est_basic"
	createBloatedIndexTable(t, conn, tableName, 50000, 50)

	results, err := analysis.DetectBtreeIndexBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectBtreeIndexBloat failed: %v", err)
	}

	idx := findIndexResult(t, results, "idx_"+tableName+"_data")
	if idx == nil {
		t.Fatalf("Index idx_%s_data not found in results (got %d indexes)", tableName, len(results))
	}

	t.Logf("Index %s: size=%s pages=%d min_pages=%d bloat_pages=%d bloat_pct=%v is_na=%v ff=%d",
		idx.IndexName, output.FormatSize(idx.IndexSize), idx.Pages, idx.MinPages,
		idx.BloatPages, pctStr(idx.BloatPct), idx.IsNA, idx.FillFactor)

	if idx.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil")
	}

	// 50% delete should give ~40-80% index bloat
	// B-Tree indexes have higher apparent bloat than heap due to internal fragmentation
	if *idx.BloatPct < 40 || *idx.BloatPct > 80 {
		t.Errorf("Expected bloat 40-80%%, got %.1f%%", *idx.BloatPct)
	}

	if idx.TableName != tableName {
		t.Errorf("Expected table name %s, got %s", tableName, idx.TableName)
	}
}

// TestBtreeEstimateHighBloat tests with 90% delete
func TestBtreeEstimateHighBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "btree_est_high"
	createBloatedIndexTable(t, conn, tableName, 50000, 90)

	results, err := analysis.DetectBtreeIndexBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectBtreeIndexBloat failed: %v", err)
	}

	idx := findIndexResult(t, results, "idx_"+tableName+"_data")
	if idx == nil {
		t.Fatalf("Index not found in results")
	}

	t.Logf("Index %s: bloat_pct=%v", idx.IndexName, pctStr(idx.BloatPct))

	if idx.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil")
	}

	// 90% delete should give ~60-95% index bloat
	if *idx.BloatPct < 60 || *idx.BloatPct > 95 {
		t.Errorf("Expected bloat 60-95%%, got %.1f%%", *idx.BloatPct)
	}
}

// TestBtreeEstimateNoBloat tests with no deletes
func TestBtreeEstimateNoBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "btree_est_nobloat"
	createBloatedIndexTable(t, conn, tableName, 50000, 0)

	results, err := analysis.DetectBtreeIndexBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectBtreeIndexBloat failed: %v", err)
	}

	idx := findIndexResult(t, results, "idx_"+tableName+"_data")
	if idx == nil {
		t.Fatalf("Index not found in results")
	}

	t.Logf("Index %s: bloat_pct=%v", idx.IndexName, pctStr(idx.BloatPct))

	if idx.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil")
	}

	// 0% delete — B-Tree statistical estimation typically shows 30-50% "bloat"
	// even on a fresh index due to internal page overhead, leaf density, and
	// the gap between avg_width estimation vs actual storage. This is expected
	// behavior for the ioguix-based estimation approach.
	if *idx.BloatPct > 55 {
		t.Errorf("Expected bloat < 55%% for fresh index, got %.1f%%", *idx.BloatPct)
	}
}

// TestBtreeEstimateIsNA tests that indexes on "name" columns are flagged unreliable
func TestBtreeEstimateIsNA(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "btree_est_isna"
	ctx := context.Background()

	conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			col_name NAME
		) WITH (autovacuum_enabled = false)
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("CREATE INDEX idx_%s_name ON %s (col_name)", tableName, tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (col_name)
		SELECT 'name_' || g
		FROM generate_series(1, 10000) g
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s", tableName))

	results, err := analysis.DetectBtreeIndexBloat(ctx, conn)
	if err != nil {
		t.Fatalf("DetectBtreeIndexBloat failed: %v", err)
	}

	idx := findIndexResult(t, results, "idx_"+tableName+"_name")
	if idx == nil {
		t.Fatalf("Index not found in results")
	}

	t.Logf("Index %s: is_na=%v bloat_pct=%v", idx.IndexName, idx.IsNA, pctStr(idx.BloatPct))

	if !idx.IsNA {
		t.Errorf("Expected IsNA=true for index on NAME column")
	}
}

// TestBtreeEstimateFillfactor tests that custom fillfactor is reported
func TestBtreeEstimateFillfactor(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "btree_est_ff"
	ctx := context.Background()

	conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		) WITH (autovacuum_enabled = false)
	`, tableName))
	// Create index with fillfactor=70
	conn.Exec(ctx, fmt.Sprintf(
		"CREATE INDEX idx_%s_data ON %s (data) WITH (fillfactor = 70)", tableName, tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data)
		SELECT 'row_' || g || '_padding_data_here'
		FROM generate_series(1, 20000) g
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("VACUUM ANALYZE %s", tableName))

	results, err := analysis.DetectBtreeIndexBloat(ctx, conn)
	if err != nil {
		t.Fatalf("DetectBtreeIndexBloat failed: %v", err)
	}

	idx := findIndexResult(t, results, "idx_"+tableName+"_data")
	if idx == nil {
		t.Fatalf("Index not found in results")
	}

	t.Logf("Index %s: fillfactor=%d bloat_pct=%v", idx.IndexName, idx.FillFactor, pctStr(idx.BloatPct))

	if idx.FillFactor != 70 {
		t.Errorf("Expected FillFactor=70, got %d", idx.FillFactor)
	}
}

// TestBtreeEstimateCLI tests B-Tree estimation via CLI
func TestBtreeEstimateCLI(t *testing.T) {
	conn := setupTestDB(t)

	tableName := "btree_cli_test"
	createBloatedIndexTable(t, conn, tableName, 50000, 50)
	conn.Close()

	out, err := runQwashCLI(t, "--estimate", "--btree")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, out)
	}

	t.Logf("CLI output:\n%s", out)

	if !strings.Contains(out, "idx_"+tableName+"_data") {
		t.Errorf("Output should contain index name 'idx_%s_data'", tableName)
	}
	if !strings.Contains(out, "INDEX") {
		t.Errorf("Output should contain 'INDEX'")
	}
}

// TestBtreeEstimateJSON tests JSON output for B-Tree estimation
func TestBtreeEstimateJSON(t *testing.T) {
	conn := setupTestDB(t)

	tableName := "btree_json_test"
	createBloatedIndexTable(t, conn, tableName, 20000, 0)
	conn.Close()

	out, err := runQwashCLI(t, "--estimate", "--btree", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, out)
	}

	t.Logf("JSON output:\n%s", out)

	var report output.BloatReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if len(report.Indexes) == 0 {
		t.Errorf("Expected non-empty indexes array in JSON output")
	}

	found := false
	for _, idx := range report.Indexes {
		if idx.IndexName == "idx_"+tableName+"_data" {
			found = true
			t.Logf("JSON index: %+v", idx)
			if idx.IndexSize <= 0 {
				t.Errorf("Expected positive index_size, got %d", idx.IndexSize)
			}
			if idx.FillFactor != 90 {
				t.Errorf("Expected default fill_factor=90, got %d", idx.FillFactor)
			}
			break
		}
	}
	if !found {
		t.Errorf("Index idx_%s_data not found in JSON output", tableName)
	}
}

// =============================================================================
// HELPERS
// =============================================================================

// findIndexResult finds a BloatIndex result by index name
// TestBtreeIndexWithoutStatsSurfaced verifies that an index whose key column
// has no statistics is still reported (flagged is_na), instead of being
// silently dropped by an INNER JOIN on pg_stats (the pre-F3 behavior).
func TestBtreeIndexWithoutStatsSurfaced(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	// SET STATISTICS 0 makes ANALYZE store no pg_stats row for column k.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE nostats_idx (id serial primary key, k int) WITH (autovacuum_enabled = false);
		INSERT INTO nostats_idx (k) SELECT g % 500 FROM generate_series(1, 8000) g;
		ALTER TABLE nostats_idx ALTER COLUMN k SET STATISTICS 0;
		CREATE INDEX nostats_idx_k ON nostats_idx (k);
		ANALYZE nostats_idx;
	`); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	results, err := analysis.DetectBtreeIndexBloat(ctx, conn)
	if err != nil {
		t.Fatalf("DetectBtreeIndexBloat failed: %v", err)
	}

	idx := findIndexResult(t, results, "nostats_idx_k")
	if idx == nil {
		t.Fatal("Index nostats_idx_k is missing from the report (silently dropped)")
	}
	if !idx.IsNA {
		t.Errorf("Index with no column statistics should be flagged is_na, got is_na=false")
	}
}

func findIndexResult(t *testing.T, results []analysis.BloatIndex, indexName string) *analysis.BloatIndex {
	t.Helper()
	for i, r := range results {
		if r.IndexName == indexName {
			return &results[i]
		}
	}
	for _, r := range results {
		t.Logf("  found index: %s.%s (table: %s)", r.Schema, r.IndexName, r.TableName)
	}
	return nil
}
