package tests

import (
	"strings"
	"testing"

	"github.com/Alain-L/qwash/db"
)

// createGrownTable creates a table with no bloat at all but misleading
// statistics: it is analyzed while holding short rows, then filled with long
// ones and vacuumed (which refreshes reltuples and relpages, not pg_stats).
// The estimate then takes the long rows for short ones and reports most of
// the table as bloat.
func createGrownTable(t *testing.T, name string) int {
	conn := setupTestDB(t)
	defer conn.Close()
	for _, q := range []string{
		"CREATE TABLE " + name + " (id serial PRIMARY KEY, v text) WITH (autovacuum_enabled = false)",
		"INSERT INTO " + name + " (v) SELECT 'x' FROM generate_series(1, 1000)",
		"ANALYZE " + name,
		"INSERT INTO " + name + " (v) SELECT repeat('y', 300) FROM generate_series(1, 30000)",
		"VACUUM " + name,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("setup failed on %q: %v", q, err)
		}
	}
	return getTablePages(t, conn, name)
}

// TestDebloatAnalyzesFirst verifies that debloat refreshes the statistics
// before estimating (regression test: a table without bloat but with stale
// statistics was rewritten for nothing, growing it and flooding the WAL).
func TestDebloatAnalyzesFirst(t *testing.T) {
	initial := createGrownTable(t, "grown_analyze")

	output, err := runQwashCLI(t, "--debloat", "-t", "grown_analyze", "--json")
	if err != nil {
		t.Fatalf("Debloat failed: %v\nOutput: %s", err, output)
	}
	if !strings.Contains(output, `"bloat_removed_pages": 0`) {
		t.Errorf("With fresh statistics the table has no bloat and must be left alone\nOutput: %s", output)
	}

	conn, err := db.Connect(getTestConfig(), false)
	if err != nil {
		t.Fatalf("Failed to reconnect: %v", err)
	}
	defer conn.Close()
	if final := getTablePages(t, conn, "grown_analyze"); final != initial {
		t.Errorf("Table should not have been rewritten: %d -> %d pages", initial, final)
	}
}

// TestDebloatStopsWithoutFreeSpace verifies that the compaction gives up on a
// table when rows keep moving to higher pages even after a VACUUM, instead of
// rewriting it end to end and making it bigger. --no-analyze keeps the stale
// estimate so the compaction is sent after space that does not exist.
func TestDebloatStopsWithoutFreeSpace(t *testing.T) {
	initial := createGrownTable(t, "grown_noanalyze")

	output, err := runQwashCLI(t, "--debloat", "-t", "grown_noanalyze", "--no-analyze")
	if err != nil {
		t.Fatalf("Running out of free space is not a failure: %v\nOutput: %s", err, output)
	}
	if !strings.Contains(output, "stopped early") {
		t.Errorf("Expected a warning that the compaction stopped early\nOutput: %s", output)
	}

	conn, err := db.Connect(getTestConfig(), false)
	if err != nil {
		t.Fatalf("Failed to reconnect: %v", err)
	}
	defer conn.Close()
	// A couple of pages may be added by the rows moved before giving up.
	if final := getTablePages(t, conn, "grown_noanalyze"); final > initial+2 {
		t.Errorf("Table grew from %d to %d pages", initial, final)
	}
}
