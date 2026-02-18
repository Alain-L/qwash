package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"qwash/analysis"
	"qwash/db"
)

// =============================================================================
// TOAST BLOAT ESTIMATION TESTS
// =============================================================================

// TestToastEstimateBasic tests TOAST bloat estimation with 50% delete
func TestToastEstimateBasic(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_basic"
	createBloatedToastTable(t, conn, tableName, 3000, 50)

	results, err := analysis.DetectToastBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectToastBloat failed: %v", err)
	}

	tb := findToastResult(t, results, tableName)
	if tb == nil {
		t.Fatalf("Table %s not found in TOAST results", tableName)
	}

	t.Logf("Table %s: toast_size=%d pages=%d chunks=%d bloat_pct=%v warning=%q",
		tableName, tb.ToastSize, tb.ToastPages, tb.ToastChunks, pctStr(tb.BloatPct), tb.Warning)

	if tb.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil (warning: %s)", tb.Warning)
	}

	// 50% delete should give ~40-60% bloat
	if *tb.BloatPct < 40 || *tb.BloatPct > 60 {
		t.Errorf("Expected bloat 40-60%%, got %.1f%%", *tb.BloatPct)
	}
}

// TestToastEstimateHighBloat tests TOAST bloat estimation with 70% delete
func TestToastEstimateHighBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_high"
	createBloatedToastTable(t, conn, tableName, 3000, 70)

	results, err := analysis.DetectToastBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectToastBloat failed: %v", err)
	}

	tb := findToastResult(t, results, tableName)
	if tb == nil {
		t.Fatalf("Table %s not found in TOAST results", tableName)
	}

	t.Logf("Table %s: bloat_pct=%v", tableName, pctStr(tb.BloatPct))

	if tb.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil (warning: %s)", tb.Warning)
	}

	// 70% delete should give ~60-80% bloat
	if *tb.BloatPct < 60 || *tb.BloatPct > 80 {
		t.Errorf("Expected bloat 60-80%%, got %.1f%%", *tb.BloatPct)
	}
}

// TestToastEstimateLowBloat tests TOAST bloat estimation with 10% delete
func TestToastEstimateLowBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_low"
	createBloatedToastTable(t, conn, tableName, 3000, 10)

	results, err := analysis.DetectToastBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectToastBloat failed: %v", err)
	}

	tb := findToastResult(t, results, tableName)
	if tb == nil {
		t.Fatalf("Table %s not found in TOAST results", tableName)
	}

	t.Logf("Table %s: bloat_pct=%v", tableName, pctStr(tb.BloatPct))

	if tb.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil (warning: %s)", tb.Warning)
	}

	// 10% delete should give ~0-20% bloat
	if *tb.BloatPct < 0 || *tb.BloatPct > 20 {
		t.Errorf("Expected bloat 0-20%%, got %.1f%%", *tb.BloatPct)
	}
}

// TestToastEstimateNoBloat tests TOAST bloat estimation with no deletes
func TestToastEstimateNoBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_nobloat"
	createBloatedToastTable(t, conn, tableName, 3000, 0)

	results, err := analysis.DetectToastBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectToastBloat failed: %v", err)
	}

	tb := findToastResult(t, results, tableName)
	if tb == nil {
		t.Fatalf("Table %s not found in TOAST results", tableName)
	}

	t.Logf("Table %s: bloat_pct=%v", tableName, pctStr(tb.BloatPct))

	if tb.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil (warning: %s)", tb.Warning)
	}

	// 0% delete should give ~0-8% bloat (baseline overhead)
	if *tb.BloatPct > 8 {
		t.Errorf("Expected bloat < 8%% for non-bloated table, got %.1f%%", *tb.BloatPct)
	}
}

// TestToastEstimateSmallTable tests that small TOAST tables are flagged unreliable
func TestToastEstimateSmallTable(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_small"
	// 100 rows × ~10KB = ~1 MB TOAST, well below 10 MB threshold
	createBloatedToastTable(t, conn, tableName, 100, 50)

	results, err := analysis.DetectToastBloat(context.Background(), conn)
	if err != nil {
		t.Fatalf("DetectToastBloat failed: %v", err)
	}

	tb := findToastResult(t, results, tableName)
	if tb == nil {
		t.Fatalf("Table %s not found in TOAST results", tableName)
	}

	t.Logf("Table %s: pages=%d bloat_pct=%v warning=%q",
		tableName, tb.ToastPages, pctStr(tb.BloatPct), tb.Warning)

	// Should be unreliable (< 10 MB)
	if tb.BloatPct != nil {
		t.Errorf("Expected nil bloat_pct for small table, got %.1f%%", *tb.BloatPct)
	}
	if tb.Warning != "< 10 MB" {
		t.Errorf("Expected warning '< 10 MB', got %q", tb.Warning)
	}
}

// TestToastEstimateCLI tests TOAST estimation via CLI
func TestToastEstimateCLI(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedToastTable(t, conn, "toast_cli_test", 3000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "--toast", "-t", "toast_cli_test")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	if !strings.Contains(output, "toast_cli_test") {
		t.Errorf("Output should contain table name 'toast_cli_test'")
	}
	if !strings.Contains(output, "TOAST") {
		t.Errorf("Output should contain 'TOAST'")
	}
}

// TestToastEstimateTwoChunkValues tests estimation accuracy for values with exactly 2 chunks.
func TestToastEstimateTwoChunkValues(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_2chunk"
	ctx := context.Background()

	// Create table with ~3KB values → 2 chunks per value (1996 + ~1009)
	_, err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data TEXT
		) WITH (autovacuum_enabled = false)
	`, tableName))
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = conn.Exec(ctx, fmt.Sprintf(
		"ALTER TABLE %s ALTER COLUMN data SET STORAGE EXTERNAL", tableName))
	if err != nil {
		t.Fatalf("Failed to set storage: %v", err)
	}
	// Exactly 3992 bytes per value → 2 full chunks of 1996 bytes each
	// Using an exact multiple avoids partial last chunks that skew avg_chunk_size
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data)
		SELECT repeat('x', 1996 * 2)
		FROM generate_series(1, 10000) g
	`, tableName))
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}
	// No deletions → 0% bloat expected
	_, err = conn.Exec(ctx, fmt.Sprintf("VACUUM %s", tableName))
	if err != nil {
		t.Fatalf("Failed to vacuum: %v", err)
	}

	results, err := analysis.DetectToastBloat(ctx, conn)
	if err != nil {
		t.Fatalf("DetectToastBloat failed: %v", err)
	}

	tb := findToastResult(t, results, tableName)
	if tb == nil {
		t.Fatalf("Table %s not found in TOAST results", tableName)
	}

	t.Logf("Table %s: toast_size=%d pages=%d chunks=%d bloat_pct=%v",
		tableName, tb.ToastSize, tb.ToastPages, tb.ToastChunks, pctStr(tb.BloatPct))

	if tb.BloatPct == nil {
		t.Fatalf("Expected bloat percentage, got nil (warning: %s)", tb.Warning)
	}

	// With 0 deletions, bloat should be < 8% (baseline overhead only)
	if *tb.BloatPct > 8 {
		t.Errorf("Expected bloat < 8%% for non-bloated 2-chunk table, got %.1f%%",
			*tb.BloatPct)
	}
}

// TestToastEstimateChunkSampling verifies that chunk sampling returns full-size chunks
func TestToastEstimateChunkSampling(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "toast_est_chunks"
	createBloatedToastTable(t, conn, tableName, 3000, 0)

	// Verify the table has multi-chunk values (chunk_seq > 0 exists)
	var maxSeq int
	oid := getTableOID(t, conn, tableName)
	err := conn.QueryRow(context.Background(),
		"SELECT max(chunk_seq) FROM pg_toast.pg_toast_"+oid).Scan(&maxSeq)
	if err != nil {
		t.Fatalf("Failed to get max chunk_seq: %v", err)
	}

	t.Logf("Table %s (OID %s): max chunk_seq = %d", tableName, oid, maxSeq)

	if maxSeq < 1 {
		t.Fatalf("Expected multi-chunk values (chunk_seq > 0), got max_seq=%d. "+
			"Test data too small for chunk sampling test.", maxSeq)
	}

	// Verify full-size chunks exist at chunk_seq > 0
	var chunkLen int
	err = conn.QueryRow(context.Background(),
		"SELECT length(chunk_data) FROM pg_toast.pg_toast_"+oid+" WHERE chunk_seq > 0 LIMIT 1").Scan(&chunkLen)
	if err != nil {
		t.Fatalf("Failed to get chunk length: %v", err)
	}

	t.Logf("Full-size chunk length: %d", chunkLen)

	// TOAST_MAX_CHUNK_SIZE on 8kB / 64-bit = 1996
	if chunkLen != 1996 {
		t.Errorf("Expected full-size chunk of 1996 bytes, got %d", chunkLen)
	}
}

// =============================================================================
// HELPERS
// =============================================================================

// findToastResult finds a ToastBloat result by table name
func findToastResult(t *testing.T, results []analysis.ToastBloat, tableName string) *analysis.ToastBloat {
	t.Helper()
	for i, r := range results {
		if r.TableName == tableName {
			return &results[i]
		}
	}
	return nil
}

// getTableOID returns the OID of a table as a string
func getTableOID(t *testing.T, conn *db.DB, tableName string) string {
	t.Helper()
	var oid string
	err := conn.QueryRow(context.Background(),
		"SELECT oid::text FROM pg_class WHERE relname = $1", tableName).Scan(&oid)
	if err != nil {
		t.Fatalf("Failed to get OID for %s: %v", tableName, err)
	}
	return oid
}

// pctStr formats a *float64 as a display string
func pctStr(pct *float64) string {
	if pct == nil {
		return "nil"
	}
	return strings.TrimRight(strings.TrimRight(
		strings.Replace(
			strings.Replace(
				fmt.Sprintf("%.1f%%", *pct), ".", ",", 1), ",", ".", 1),
		"0"), ".")
}
