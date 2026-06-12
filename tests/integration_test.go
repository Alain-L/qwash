package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"qwash/db"

	"github.com/jackc/pgx/v5"
)

// Test database configuration from environment or defaults
func getTestConfig() db.Config {
	return db.Config{
		Host:     getEnv("PGHOST", "localhost"),
		Port:     getEnv("PGPORT", "5432"),
		User:     getEnv("PGUSER", "postgres"),
		Password: getEnv("PGPASSWORD", "postgres"),
		Database: getEnv("PGDATABASE", "qwash_test"),
		SSLMode:  getEnv("PGSSLMODE", "disable"),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// setupTestDB creates a fresh test database
func setupTestDB(t *testing.T) *db.DB {
	cfg := getTestConfig()

	// Connect to postgres database to create test database
	adminCfg := cfg
	adminCfg.Database = "postgres"
	adminConn, err := pgx.Connect(context.Background(),
		fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=%s",
			adminCfg.User, adminCfg.Password, adminCfg.Host, adminCfg.Port, adminCfg.Database, adminCfg.SSLMode))
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}

	// Drop and recreate test database
	adminConn.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s", cfg.Database))
	_, err = adminConn.Exec(context.Background(), fmt.Sprintf("CREATE DATABASE %s", cfg.Database))
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	adminConn.Close(context.Background())

	// Connect to test database
	conn, err := db.Connect(cfg, false)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	return conn
}

// createBloatedTable creates a table with deterministic bloat
// rows: total rows to insert
// deletePercent: percentage of rows to delete (e.g., 50 means delete 50%)
func createBloatedTable(t *testing.T, conn *db.DB, tableName string, rows int, deletePercent int) {
	ctx := context.Background()

	// Drop if exists
	_, err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	// Create table with autovacuum disabled
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		) WITH (autovacuum_enabled = false)
	`, tableName))
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// Insert deterministic data
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data)
		SELECT 'row_' || g || '_padding_data_here'
		FROM generate_series(1, %d) g
	`, tableName, rows))
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Delete specific rows to create bloat (deterministic: delete where id %% 100 < deletePercent)
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %s WHERE id %% 100 < %d
	`, tableName, deletePercent))
	if err != nil {
		t.Fatalf("Failed to delete rows: %v", err)
	}

	// ANALYZE to update statistics
	_, err = conn.Exec(ctx, fmt.Sprintf("ANALYZE %s", tableName))
	if err != nil {
		t.Fatalf("Failed to analyze table: %v", err)
	}
}

// createBloatedToastTable creates a table with TOAST bloat for testing.
// Uses ~10 KB TEXT values (STORAGE EXTERNAL) → ~5 chunks per value at 1996 bytes/chunk.
// rows: total rows to insert (minimum ~2000 for reliable estimation above 10 MB threshold)
// deletePercent: percentage of rows to delete (e.g., 50 means delete 50%)
func createBloatedToastTable(t *testing.T, conn *db.DB, tableName string, rows int, deletePercent int) {
	ctx := context.Background()

	// Drop if exists
	_, err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	// Create table with a TEXT column and autovacuum disabled
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data TEXT
		) WITH (autovacuum_enabled = false)
	`, tableName))
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// Force external storage (no compression) for predictable chunk sizes
	_, err = conn.Exec(ctx, fmt.Sprintf(
		"ALTER TABLE %s ALTER COLUMN data SET STORAGE EXTERNAL", tableName))
	if err != nil {
		t.Fatalf("Failed to set storage external: %v", err)
	}

	// Insert exactly 11976 bytes per row → 6 full chunks of 1996 bytes each
	// Using an exact multiple of TOAST_MAX_CHUNK_SIZE avoids partial last chunks
	// that would skew avg(length(chunk_data)) below 1996
	_, err = conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data)
		SELECT repeat('x', 1996 * 6)
		FROM generate_series(1, %d) g
	`, tableName, rows))
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Delete specific rows to create TOAST bloat
	if deletePercent > 0 {
		_, err = conn.Exec(ctx, fmt.Sprintf(`
			DELETE FROM %s WHERE id %% 100 < %d
		`, tableName, deletePercent))
		if err != nil {
			t.Fatalf("Failed to delete rows: %v", err)
		}
	}

	// VACUUM to update pg_class stats for TOAST table (ANALYZE alone doesn't work for TOAST)
	_, err = conn.Exec(ctx, fmt.Sprintf("VACUUM %s", tableName))
	if err != nil {
		t.Fatalf("Failed to vacuum table: %v", err)
	}
}

// getBloatPages returns the estimated bloat pages for a table
func getBloatPages(t *testing.T, conn *db.DB, tableName string) int {
	pages, err := conn.GetBloatPages(tableName)
	if err != nil {
		t.Fatalf("Failed to get bloat pages: %v", err)
	}
	return pages
}

// getTablePages returns the actual page count for a table
func getTablePages(t *testing.T, conn *db.DB, tableName string) int {
	pages, err := conn.GetTablePages(tableName)
	if err != nil {
		t.Fatalf("Failed to get table pages: %v", err)
	}
	return pages
}

// runQwashCLI runs the qwash binary with given arguments and returns output/error
func runQwashCLI(t *testing.T, args ...string) (string, error) {
	cfg := getTestConfig()

	// Build base args with connection info
	baseArgs := []string{
		"-H", cfg.Host,
		"-P", cfg.Port,
		"-U", cfg.User,
		"-d", cfg.Database,
		"--sslmode", cfg.SSLMode,
	}
	if cfg.Password != "" {
		baseArgs = append(baseArgs, "-W", cfg.Password)
	}

	allArgs := append(baseArgs, args...)
	cmd := exec.Command("./bin/qwash", allArgs...)
	cmd.Dir = ".." // Run from project root

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// =============================================================================
// DEBLOAT MODE TESTS
// =============================================================================

// TestDebloatDefault tests the default debloat mode
func TestDebloatDefault(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_debloat_default"
	createBloatedTable(t, conn, tableName, 10000, 50) // 10k rows, 50% deleted

	// Set progress tracking
	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, tableName)
	initialBloat := getBloatPages(t, conn, tableName)

	t.Logf("Initial state: %d pages, %d bloat pages", initialPages, initialBloat)

	if initialBloat < 5 {
		t.Skip("Not enough bloat to test (need at least 5 pages)")
	}

	// Run debloat (2 passes like default mode)
	for pass := 1; pass <= 2; pass++ {
		err := conn.CompactTableUpdate(tableName)
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	finalPages := getTablePages(t, conn, tableName)
	finalBloat := getBloatPages(t, conn, tableName)

	t.Logf("Final state: %d pages, %d bloat pages", finalPages, finalBloat)

	// Verify bloat was reduced
	if finalPages >= initialPages {
		t.Errorf("Expected page count to decrease: initial=%d, final=%d", initialPages, finalPages)
	}

	// Verify significant reduction (at least 80% of bloat removed)
	reduction := float64(initialBloat-finalBloat) / float64(initialBloat) * 100
	if reduction < 80 {
		t.Errorf("Expected at least 80%% bloat reduction, got %.1f%%", reduction)
	}

	t.Logf("Bloat reduction: %.1f%%", reduction)
}

// TestDebloatFast tests the fast debloat mode
func TestDebloatFast(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_debloat_fast"
	createBloatedTable(t, conn, tableName, 10000, 50)

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, tableName)
	initialBloat := getBloatPages(t, conn, tableName)

	t.Logf("Initial state: %d pages, %d bloat pages", initialPages, initialBloat)

	if initialBloat < 5 {
		t.Skip("Not enough bloat to test")
	}

	// Run fast debloat (1 pass)
	err := conn.CompactTableUpdate(tableName)
	if err != nil {
		t.Fatalf("CompactTableUpdate failed: %v", err)
	}

	finalPages := getTablePages(t, conn, tableName)
	finalBloat := getBloatPages(t, conn, tableName)

	t.Logf("Final state: %d pages, %d bloat pages", finalPages, finalBloat)

	// Verify bloat was reduced
	if finalPages >= initialPages {
		t.Errorf("Expected page count to decrease: initial=%d, final=%d", initialPages, finalPages)
	}

	// Fast mode targets <10% bloat remaining
	remainingPct := float64(finalBloat) / float64(finalPages) * 100
	if remainingPct > 15 { // Allow some margin
		t.Errorf("Expected bloat < 15%%, got %.1f%%", remainingPct)
	}

	t.Logf("Remaining bloat: %.1f%%", remainingPct)
}

// TestDebloatSlow tests the slow debloat mode
func TestDebloatSlow(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_debloat_slow"
	// Smaller table for slow mode (it's slow!)
	createBloatedTable(t, conn, tableName, 2000, 50)

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, tableName)
	initialBloat := getBloatPages(t, conn, tableName)

	t.Logf("Initial state: %d pages, %d bloat pages", initialPages, initialBloat)

	if initialBloat < 3 {
		t.Skip("Not enough bloat to test")
	}

	// Run slow debloat (3 passes like slow mode)
	for pass := 1; pass <= 3; pass++ {
		err := conn.CompactTableUpdate(tableName)
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	finalPages := getTablePages(t, conn, tableName)
	finalBloat := getBloatPages(t, conn, tableName)

	t.Logf("Final state: %d pages, %d bloat pages", finalPages, finalBloat)

	// Verify bloat was reduced
	if finalPages >= initialPages {
		t.Errorf("Expected page count to decrease: initial=%d, final=%d", initialPages, finalPages)
	}

	// Verify significant reduction
	reduction := float64(initialBloat-finalBloat) / float64(initialBloat) * 100
	if reduction < 80 {
		t.Errorf("Expected at least 80%% bloat reduction, got %.1f%%", reduction)
	}

	t.Logf("Bloat reduction: %.1f%%", reduction)
}

// TestDebloatSlowWithDelay tests the slow debloat mode with custom delay
func TestDebloatSlowWithDelay(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_debloat_slow_delay"
	// Very small table since we're testing with delay
	createBloatedTable(t, conn, tableName, 1000, 50)

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, tableName)
	initialBloat := getBloatPages(t, conn, tableName)

	t.Logf("Initial state: %d pages, %d bloat pages", initialPages, initialBloat)

	if initialBloat < 2 {
		t.Skip("Not enough bloat to test")
	}

	// Run slow debloat (3 passes like slow mode with delay)
	conn.DelayMs = 5
	for pass := 1; pass <= 3; pass++ {
		err := conn.CompactTableUpdate(tableName)
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	finalPages := getTablePages(t, conn, tableName)
	finalBloat := getBloatPages(t, conn, tableName)

	t.Logf("Final state: %d pages, %d bloat pages", finalPages, finalBloat)

	// Verify bloat was reduced
	if finalPages >= initialPages {
		t.Errorf("Expected page count to decrease: initial=%d, final=%d", initialPages, finalPages)
	}
}

// TestDelayActuallyThrottles verifies that DelayMs introduces a real pause
// between page rounds (regression test: DelayMs used to be silently ignored,
// so --slow --delay ran at full speed).
func TestDelayActuallyThrottles(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_delay_throttle"
	createBloatedTable(t, conn, tableName, 2000, 50)

	bloatPages := getBloatPages(t, conn, tableName)
	if bloatPages < 5 {
		t.Skip("Not enough bloat to measure throttling")
	}

	// One compaction pass processes roughly bloatPages page rounds, each of
	// which must sleep DelayMs. Assert on half of that as a safety margin
	// against estimation drift between this call and the one made inside
	// CompactTableUpdate.
	conn.DelayMs = 20
	start := time.Now()
	if err := conn.CompactTableUpdate(tableName); err != nil {
		t.Fatalf("CompactTableUpdate failed: %v", err)
	}
	elapsed := time.Since(start)

	minExpected := time.Duration(bloatPages/2) * 20 * time.Millisecond
	if elapsed < minExpected {
		t.Errorf("Expected at least %v of throttling for %d bloat pages with 20ms delay, took %v",
			minExpected, bloatPages, elapsed)
	}
	t.Logf("Compaction with 20ms delay over ~%d bloat pages took %v", bloatPages, elapsed)
}

// =============================================================================
// TABLE SELECTION TESTS (-t and -X flags)
// =============================================================================

// TestDebloatSpecificTable tests debloating a specific table with -t flag
func TestDebloatSpecificTable(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	// Create multiple tables
	createBloatedTable(t, conn, "table_target", 5000, 50)
	createBloatedTable(t, conn, "table_other", 5000, 50)

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	targetInitialPages := getTablePages(t, conn, "table_target")
	otherInitialPages := getTablePages(t, conn, "table_other")
	targetInitialBloat := getBloatPages(t, conn, "table_target")

	t.Logf("Target table: %d pages, %d bloat", targetInitialPages, targetInitialBloat)
	t.Logf("Other table: %d pages", otherInitialPages)

	if targetInitialBloat < 3 {
		t.Skip("Not enough bloat to test")
	}

	// Debloat only the target table (2 passes)
	for pass := 1; pass <= 2; pass++ {
		err := conn.CompactTableUpdate("table_target")
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	targetFinalPages := getTablePages(t, conn, "table_target")
	otherFinalPages := getTablePages(t, conn, "table_other")

	t.Logf("Target final: %d pages", targetFinalPages)
	t.Logf("Other final: %d pages", otherFinalPages)

	// Target should be compacted
	if targetFinalPages >= targetInitialPages {
		t.Errorf("Target table should have fewer pages: initial=%d, final=%d", targetInitialPages, targetFinalPages)
	}

	// Other table should be unchanged
	if otherFinalPages != otherInitialPages {
		t.Errorf("Other table should be unchanged: initial=%d, final=%d", otherInitialPages, otherFinalPages)
	}
}

// TestDebloatMultipleTables tests debloating multiple tables
func TestDebloatMultipleTables(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tables := []struct {
		name          string
		rows          int
		deletePercent int
	}{
		{"test_multi_1", 5000, 30},
		{"test_multi_2", 5000, 50},
		{"test_multi_3", 5000, 70},
	}

	// Create all tables
	for _, tbl := range tables {
		createBloatedTable(t, conn, tbl.name, tbl.rows, tbl.deletePercent)
	}

	// Set total tables for progress tracking
	conn.TotalTables = len(tables)

	// Debloat each and verify
	for i, tbl := range tables {
		conn.CurrentTableIndex = i + 1

		initialBloat := getBloatPages(t, conn, tbl.name)
		initialPages := getTablePages(t, conn, tbl.name)

		if initialBloat < 3 {
			t.Logf("Skipping %s: not enough bloat", tbl.name)
			continue
		}

		t.Logf("%s: initial %d pages, %d bloat", tbl.name, initialPages, initialBloat)

		// Run 2 passes like default mode
		var compactErr error
		for pass := 1; pass <= 2; pass++ {
			compactErr = conn.CompactTableUpdate(tbl.name)
			if compactErr != nil {
				break
			}
		}
		if compactErr != nil {
			t.Errorf("CompactTableUpdate failed for %s: %v", tbl.name, compactErr)
			continue
		}

		finalPages := getTablePages(t, conn, tbl.name)
		finalBloat := getBloatPages(t, conn, tbl.name)

		t.Logf("%s: final %d pages, %d bloat", tbl.name, finalPages, finalBloat)

		if finalPages >= initialPages {
			t.Errorf("%s: expected page reduction, got initial=%d final=%d", tbl.name, initialPages, finalPages)
		}
	}
}

// =============================================================================
// EDGE CASES
// =============================================================================

// TestDebloatNoBloat tests behavior when table has no bloat
func TestDebloatNoBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_no_bloat"
	ctx := context.Background()

	// Create table without bloat
	conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		)
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data)
		SELECT 'row_' || g
		FROM generate_series(1, 1000) g
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("VACUUM %s", tableName))
	conn.Exec(ctx, fmt.Sprintf("ANALYZE %s", tableName))

	bloatPages := getBloatPages(t, conn, tableName)

	t.Logf("Bloat pages: %d", bloatPages)

	// With no deletions, bloat should be minimal (0-2 pages)
	if bloatPages > 2 {
		t.Errorf("Expected minimal bloat (0-2 pages), got %d", bloatPages)
	}
}

// TestDebloatEmptyTable tests behavior with an empty table
func TestDebloatEmptyTable(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_empty"
	ctx := context.Background()

	// Create empty table
	conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		)
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("ANALYZE %s", tableName))

	pages := getTablePages(t, conn, tableName)

	t.Logf("Empty table pages: %d", pages)

	// Empty table should have 0 pages
	if pages != 0 {
		t.Errorf("Expected 0 pages for empty table, got %d", pages)
	}
}

// TestDebloatHighBloat tests behavior with very high bloat (90%)
func TestDebloatHighBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_high_bloat"
	createBloatedTable(t, conn, tableName, 10000, 90) // 90% deleted

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, tableName)
	initialBloat := getBloatPages(t, conn, tableName)

	t.Logf("Initial state: %d pages, %d bloat pages (%.1f%%)",
		initialPages, initialBloat, float64(initialBloat)/float64(initialPages)*100)

	if initialBloat < 10 {
		t.Skip("Not enough bloat to test")
	}

	// Run debloat (2 passes like default mode)
	for pass := 1; pass <= 2; pass++ {
		err := conn.CompactTableUpdate(tableName)
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	finalPages := getTablePages(t, conn, tableName)
	finalBloat := getBloatPages(t, conn, tableName)

	t.Logf("Final state: %d pages, %d bloat pages", finalPages, finalBloat)

	// With 90% bloat, we should see dramatic reduction
	reduction := float64(initialPages-finalPages) / float64(initialPages) * 100
	if reduction < 70 {
		t.Errorf("Expected at least 70%% page reduction for high bloat, got %.1f%%", reduction)
	}

	t.Logf("Page reduction: %.1f%%", reduction)
}

// TestDebloatLowBloat tests behavior with low bloat (10%)
func TestDebloatLowBloat(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	tableName := "test_low_bloat"
	createBloatedTable(t, conn, tableName, 10000, 10) // 10% deleted

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, tableName)
	initialBloat := getBloatPages(t, conn, tableName)

	t.Logf("Initial state: %d pages, %d bloat pages (%.1f%%)",
		initialPages, initialBloat, float64(initialBloat)/float64(initialPages)*100)

	if initialBloat < 2 {
		t.Skip("Not enough bloat to test")
	}

	// Run debloat (2 passes like default mode)
	for pass := 1; pass <= 2; pass++ {
		err := conn.CompactTableUpdate(tableName)
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	finalPages := getTablePages(t, conn, tableName)
	finalBloat := getBloatPages(t, conn, tableName)

	t.Logf("Final state: %d pages, %d bloat pages", finalPages, finalBloat)

	// Should still reduce bloat even if small
	if finalPages > initialPages {
		t.Errorf("Page count should not increase: initial=%d, final=%d", initialPages, finalPages)
	}
}

// =============================================================================
// CLI FLAG TESTS (using binary)
// =============================================================================

// TestCLIDebloatWithTableFlag tests CLI with -t flag
func TestCLIDebloatWithTableFlag(t *testing.T) {
	// First setup database and table
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "cli_test_table", 5000, 50)
	initialPages := getTablePages(t, conn, "cli_test_table")
	conn.Close()

	// Run CLI
	output, err := runQwashCLI(t, "--debloat", "-t", "cli_test_table")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	// Verify table was compacted
	conn2, _ := db.Connect(getTestConfig(), false)
	defer conn2.Close()
	finalPages := getTablePages(t, conn2, "cli_test_table")

	if finalPages >= initialPages {
		t.Errorf("Expected page reduction: initial=%d, final=%d", initialPages, finalPages)
	}
}

// TestCLIDebloatFastMode tests CLI with --fast flag
func TestCLIDebloatFastMode(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "cli_fast_table", 5000, 50)
	initialPages := getTablePages(t, conn, "cli_fast_table")
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--fast", "-t", "cli_fast_table")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	// Verify output mentions fast mode
	if !strings.Contains(output, "fast") && !strings.Contains(output, "Mode") {
		t.Log("Warning: Output doesn't clearly indicate fast mode")
	}

	conn2, _ := db.Connect(getTestConfig(), false)
	defer conn2.Close()
	finalPages := getTablePages(t, conn2, "cli_fast_table")

	if finalPages >= initialPages {
		t.Errorf("Expected page reduction: initial=%d, final=%d", initialPages, finalPages)
	}
}

// TestCLIDebloatSlowMode tests CLI with --slow flag
func TestCLIDebloatSlowMode(t *testing.T) {
	conn := setupTestDB(t)
	// Small table for slow mode
	createBloatedTable(t, conn, "cli_slow_table", 1000, 50)
	initialPages := getTablePages(t, conn, "cli_slow_table")
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--slow", "-t", "cli_slow_table")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	conn2, _ := db.Connect(getTestConfig(), false)
	defer conn2.Close()
	finalPages := getTablePages(t, conn2, "cli_slow_table")

	if finalPages >= initialPages {
		t.Errorf("Expected page reduction: initial=%d, final=%d", initialPages, finalPages)
	}
}

// TestCLIDebloatSlowWithDelay tests CLI with --slow --delay flags
func TestCLIDebloatSlowWithDelay(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "cli_delay_table", 500, 50)
	initialPages := getTablePages(t, conn, "cli_delay_table")
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--slow", "--delay", "5", "-t", "cli_delay_table")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	conn2, _ := db.Connect(getTestConfig(), false)
	defer conn2.Close()
	finalPages := getTablePages(t, conn2, "cli_delay_table")

	if finalPages >= initialPages {
		t.Errorf("Expected page reduction: initial=%d, final=%d", initialPages, finalPages)
	}
}

// =============================================================================
// ERROR CASES (CLI should fail)
// =============================================================================

// TestCLIErrorFastAndSlow tests that --fast and --slow together fails
func TestCLIErrorFastAndSlow(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "error_test_table", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--fast", "--slow", "-t", "error_test_table")

	// Should fail
	if err == nil {
		t.Errorf("Expected error when using --fast and --slow together, but got success\nOutput: %s", output)
	}

	// Should mention the conflict
	if !strings.Contains(strings.ToLower(output), "fast") || !strings.Contains(strings.ToLower(output), "slow") {
		t.Logf("Warning: Error message doesn't clearly mention the flag conflict\nOutput: %s", output)
	}
}

// TestCLIErrorDelayWithoutSlow tests that --delay without --slow fails
func TestCLIErrorDelayWithoutSlow(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "delay_error_table", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--delay", "10", "-t", "delay_error_table")

	// Should fail
	if err == nil {
		t.Errorf("Expected error when using --delay without --slow, but got success\nOutput: %s", output)
	}

	// Should mention --slow requirement
	if !strings.Contains(strings.ToLower(output), "slow") {
		t.Logf("Warning: Error message doesn't mention --slow requirement\nOutput: %s", output)
	}
}

// TestCLIErrorJobsWithoutDebloat tests that --jobs without --debloat fails
func TestCLIErrorJobsWithoutDebloat(t *testing.T) {
	conn := setupTestDB(t)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "--jobs", "4")

	// Should fail
	if err == nil {
		t.Errorf("Expected error when using --jobs without --debloat, but got success\nOutput: %s", output)
	}

	// Should mention --debloat requirement
	if !strings.Contains(strings.ToLower(output), "debloat") {
		t.Logf("Warning: Error message doesn't mention --debloat requirement\nOutput: %s", output)
	}
}

// TestCLIErrorDebloatWithToast tests that --debloat rejects --toast and --btree
// (only heap debloat is implemented; silently debloating the heap instead
// would be misleading)
func TestCLIErrorDebloatWithToast(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "toast_error_table", 1000, 50)
	conn.Close()

	for _, flag := range []string{"--toast", "--btree"} {
		output, err := runQwashCLI(t, "--debloat", flag, "-t", "toast_error_table")

		// Should fail
		if err == nil {
			t.Errorf("Expected error when using %s with --debloat, but got success\nOutput: %s", flag, output)
		}

		// Should mention the unsupported combination
		if !strings.Contains(strings.ToLower(output), "not supported") {
			t.Logf("Warning: Error message doesn't clearly mention the unsupported combination\nOutput: %s", output)
		}
	}
}

// TestCLIErrorDebloatWithoutTable tests that --debloat without -t fails:
// debloating the whole database must be requested explicitly with --all
func TestCLIErrorDebloatWithoutTable(t *testing.T) {
	conn := setupTestDB(t)
	// Create some tables
	createBloatedTable(t, conn, "auto_table_1", 1000, 50)
	createBloatedTable(t, conn, "auto_table_2", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat")

	// Should fail: neither -t nor --all was given
	if err == nil {
		t.Errorf("Expected error when using --debloat without -t or --all, but got success\nOutput: %s", output)
	}

	// Should mention the --all escape hatch
	if !strings.Contains(output, "--all") {
		t.Logf("Warning: Error message doesn't mention --all\nOutput: %s", output)
	}
}

// TestCLIDebloatAll tests that --debloat --all explicitly processes all tables
func TestCLIDebloatAll(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "all_table_1", 1000, 50)
	createBloatedTable(t, conn, "all_table_2", 1000, 50)
	initial1 := getTablePages(t, conn, "all_table_1")
	initial2 := getTablePages(t, conn, "all_table_2")
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--all")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	conn2, _ := db.Connect(getTestConfig(), false)
	defer conn2.Close()
	if final := getTablePages(t, conn2, "all_table_1"); final >= initial1 {
		t.Errorf("Expected page reduction on all_table_1: initial=%d, final=%d", initial1, final)
	}
	if final := getTablePages(t, conn2, "all_table_2"); final >= initial2 {
		t.Errorf("Expected page reduction on all_table_2: initial=%d, final=%d", initial2, final)
	}
}

// TestCLIErrorAllWithTable tests that --all and -t are mutually exclusive
func TestCLIErrorAllWithTable(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "all_conflict_table", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--all", "-t", "all_conflict_table")

	// Should fail
	if err == nil {
		t.Errorf("Expected error when using --all with -t, but got success\nOutput: %s", output)
	}
}

// =============================================================================
// LIMIT FLAG TESTS
// =============================================================================

// TestCLIDebloatWithLimitSize tests --limit with size value
func TestCLIDebloatWithLimitSize(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "limit_size_table", 10000, 50)
	conn.Close()

	// Limit to 100KB removed
	output, err := runQwashCLI(t, "--debloat", "--limit", "100KB", "-t", "limit_size_table")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	// Output should mention limit was reached or show removal amount
	if strings.Contains(output, "limit reached") {
		t.Log("Limit was reached as expected")
	}
}

// TestCLIDebloatWithLimitPercent tests --limit with percentage value
func TestCLIDebloatWithLimitPercent(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "limit_pct_table", 10000, 50)
	conn.Close()

	// Limit to 50% of bloat removed
	output, err := runQwashCLI(t, "--debloat", "--limit", "50%", "-t", "limit_pct_table")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)
}

// =============================================================================
// SCHEMA-QUALIFIED TABLE TESTS
// =============================================================================

// TestDebloatSchemaQualifiedTable tests debloating with schema.table format
func TestDebloatSchemaQualifiedTable(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	ctx := context.Background()

	// Create a custom schema
	conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS custom_schema")

	// Create table in custom schema
	conn.Exec(ctx, "DROP TABLE IF EXISTS custom_schema.schema_test")
	conn.Exec(ctx, `
		CREATE TABLE custom_schema.schema_test (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		) WITH (autovacuum_enabled = false)
	`)
	conn.Exec(ctx, `
		INSERT INTO custom_schema.schema_test (data)
		SELECT 'row_' || g || '_padding_data_here'
		FROM generate_series(1, 5000) g
	`)
	conn.Exec(ctx, "DELETE FROM custom_schema.schema_test WHERE id % 100 < 50")
	conn.Exec(ctx, "ANALYZE custom_schema.schema_test")

	conn.TotalTables = 1
	conn.CurrentTableIndex = 1

	initialPages := getTablePages(t, conn, "custom_schema.schema_test")
	initialBloat := getBloatPages(t, conn, "custom_schema.schema_test")

	t.Logf("Initial state: %d pages, %d bloat", initialPages, initialBloat)

	if initialBloat < 3 {
		t.Skip("Not enough bloat to test")
	}

	// Run 2 passes like default mode
	for pass := 1; pass <= 2; pass++ {
		err := conn.CompactTableUpdate("custom_schema.schema_test")
		if err != nil {
			t.Fatalf("CompactTableUpdate pass %d failed: %v", pass, err)
		}
	}

	finalPages := getTablePages(t, conn, "custom_schema.schema_test")

	t.Logf("Final state: %d pages", finalPages)

	if finalPages >= initialPages {
		t.Errorf("Expected page reduction: initial=%d, final=%d", initialPages, finalPages)
	}
}
