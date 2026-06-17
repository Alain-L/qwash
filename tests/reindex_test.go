package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Alain-L/qwash/db"
)

// =============================================================================
// REINDEX TESTS
// =============================================================================

// TestReindexFlag tests that --reindex flag properly rebuilds indexes
func TestReindexFlag(t *testing.T) {
	conn := setupTestDB(t)
	ctx := context.Background()

	tableName := "reindex_test_table"

	// Create table with multiple indexes
	conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100),
			value INT
		) WITH (autovacuum_enabled = false)
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("CREATE INDEX idx_%s_data ON %s(data)", tableName, tableName))
	conn.Exec(ctx, fmt.Sprintf("CREATE INDEX idx_%s_value ON %s(value)", tableName, tableName))

	// Insert data and create bloat
	conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data, value)
		SELECT 'row_' || g || '_padding_data', (random() * 1000)::int
		FROM generate_series(1, 5000) g
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id %% 10 < 7", tableName))
	conn.Exec(ctx, fmt.Sprintf("ANALYZE %s", tableName))

	// Get index OIDs before reindex
	indexOIDsBefore := getIndexOIDs(t, conn, tableName)
	t.Logf("Index OIDs before: %v", indexOIDsBefore)

	if len(indexOIDsBefore) < 2 {
		t.Fatalf("Expected at least 2 indexes, got %d", len(indexOIDsBefore))
	}

	conn.Close()

	// Run debloat with --reindex
	output, err := runQwashCLI(t, "--debloat", "-t", tableName, "--reindex")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("CLI output:\n%s", output)

	// Reconnect and get index OIDs after reindex
	conn2, err := db.Connect(getTestConfig(), false)
	if err != nil {
		t.Fatalf("Failed to reconnect: %v", err)
	}
	defer conn2.Close()

	indexOIDsAfter := getIndexOIDs(t, conn2, tableName)
	t.Logf("Index OIDs after: %v", indexOIDsAfter)

	// Verify OIDs changed (REINDEX CONCURRENTLY creates new indexes with new OIDs)
	oidsChanged := false
	for name, oidBefore := range indexOIDsBefore {
		if oidAfter, ok := indexOIDsAfter[name]; ok {
			if oidBefore != oidAfter {
				oidsChanged = true
				t.Logf("Index %s OID changed: %d -> %d", name, int64(oidBefore), int64(oidAfter))
			}
		}
	}

	if !oidsChanged {
		t.Errorf("Expected index OIDs to change after REINDEX CONCURRENTLY, but they remained the same")
	}
}

// TestReindexFlagJSON tests that --reindex flag shows reindexed:true in JSON output
func TestReindexFlagJSON(t *testing.T) {
	conn := setupTestDB(t)
	ctx := context.Background()

	tableName := "reindex_json_table"

	// Create table with index
	conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		) WITH (autovacuum_enabled = false)
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("CREATE INDEX idx_%s_data ON %s(data)", tableName, tableName))

	// Insert data and create bloat
	conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (data)
		SELECT 'row_' || g || '_padding_data_here'
		FROM generate_series(1, 5000) g
	`, tableName))
	conn.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id %% 10 < 7", tableName))
	conn.Exec(ctx, fmt.Sprintf("ANALYZE %s", tableName))

	conn.Close()

	// Run debloat with --reindex --json
	output, err := runQwashCLI(t, "--debloat", "-t", tableName, "--reindex", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	t.Logf("JSON output:\n%s", output)

	// Parse JSON and verify reindexed field
	var result struct {
		Results []struct {
			Table     string `json:"table"`
			Reindexed bool   `json:"reindexed"`
		} `json:"results"`
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if len(result.Results) == 0 {
		t.Fatal("No results in JSON output")
	}

	if !result.Results[0].Reindexed {
		t.Errorf("Expected reindexed=true in JSON output, got false")
	}
}

// TestReindexWithoutDebloat tests that --reindex without --debloat fails
func TestReindexWithoutDebloat(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "reindex_error_table", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--reindex", "-t", "reindex_error_table")

	// Should fail
	if err == nil {
		t.Errorf("Expected error when using --reindex without --debloat, but got success\nOutput: %s", output)
	}

	// Should mention --debloat requirement
	if !strings.Contains(strings.ToLower(output), "debloat") {
		t.Logf("Warning: Error message doesn't mention --debloat requirement\nOutput: %s", output)
	}
}

// getIndexOIDs returns a map of index name to OID for a given table
func getIndexOIDs(t *testing.T, conn *db.DB, tableName string) map[string]uint32 {
	ctx := context.Background()
	result := make(map[string]uint32)

	rows, err := conn.Query(ctx, `
		SELECT indexrelid::regclass::text as index_name, indexrelid::bigint as oid
		FROM pg_stat_user_indexes
		WHERE relname = $1
	`, tableName)
	if err != nil {
		t.Fatalf("Failed to query index OIDs: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var oid int64
		if err := rows.Scan(&name, &oid); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}
		result[name] = uint32(oid)
	}

	return result
}
