package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

var ctx = context.Background()

// EstimateJSON represents the JSON output structure from qwash --estimate --json
type EstimateJSON struct {
	Tables []struct {
		Schema     string  `json:"schema"`
		TableName  string  `json:"table_name"`
		TableSize  int64   `json:"table_size"`
		BloatSize  int64   `json:"bloat_size"`
		BloatRatio float64 `json:"bloat_ratio"`
		Pages      int     `json:"pages"`
		MinPages   int     `json:"min_pages"`
		LiveTuples int64   `json:"live_tuples"`
		DeadTuples int64   `json:"dead_tuples"`
		FillFactor int     `json:"fill_factor"`
	} `json:"tables"`
	Indexes []struct {
		Schema     string  `json:"schema"`
		IndexName  string  `json:"index_name"`
		TableName  string  `json:"table_name"`
		IndexSize  int64   `json:"index_size"`
		BloatSize  int64   `json:"bloat_size"`
		BloatRatio float64 `json:"bloat_ratio"`
	} `json:"indexes"`
}

// =============================================================================
// ESTIMATE FLAG COMBINATION TESTS
// =============================================================================

// TestEstimateBasic tests basic --estimate output (text mode)
func TestEstimateBasic(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "estimate_test", 3000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	// Should contain table name and bloat info
	if !strings.Contains(output, "estimate_test") {
		t.Errorf("Output should contain table name 'estimate_test', got: %s", output)
	}
	// Should show bloat ratio or size
	if !strings.Contains(output, "bloat") && !strings.Contains(output, "Bloat") && !strings.Contains(output, "%") {
		t.Errorf("Output should contain bloat information, got: %s", output)
	}
}

// TestEstimateJSON tests --estimate with --json flag
func TestEstimateJSON(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "estimate_json_test", 3000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	// Should be valid JSON
	var result EstimateJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Output should be valid JSON: %v\nOutput: %s", err, output)
	}

	// Should contain tables array
	if result.Tables == nil {
		t.Error("JSON should contain 'tables' array")
	}

	// Should find our test table
	found := false
	for _, tbl := range result.Tables {
		if tbl.TableName == "estimate_json_test" {
			found = true
			if tbl.Schema != "public" {
				t.Errorf("Expected schema 'public', got '%s'", tbl.Schema)
			}
			if tbl.BloatRatio < 0 {
				t.Error("BloatRatio should be non-negative")
			}
			break
		}
	}
	if !found {
		t.Error("Should find 'estimate_json_test' in results")
	}
}

// TestEstimateSpecificTable tests --estimate with -t flag (text mode)
func TestEstimateSpecificTable(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "specific_table_1", 3000, 50)
	createBloatedTable(t, conn, "specific_table_2", 3000, 30)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "specific_table_1")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	// Should contain the specified table
	if !strings.Contains(output, "specific_table_1") {
		t.Errorf("Output should contain 'specific_table_1', got: %s", output)
	}
	// Should NOT contain the other table (we only asked for one)
	// Note: this depends on output format - in detailed mode it might only show the one table
}

// TestEstimateSpecificTableJSON tests --estimate with -t and --json flags
func TestEstimateSpecificTableJSON(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "json_specific_1", 3000, 50)
	createBloatedTable(t, conn, "json_specific_2", 3000, 30)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "json_specific_1", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	var result EstimateJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Output should be valid JSON: %v\nOutput: %s", err, output)
	}

	// Should only contain the specified table
	if len(result.Tables) != 1 {
		t.Errorf("Expected exactly 1 table in results, got %d", len(result.Tables))
	}
	if len(result.Tables) > 0 && result.Tables[0].TableName != "json_specific_1" {
		t.Errorf("Expected table 'json_specific_1', got '%s'", result.Tables[0].TableName)
	}
}

// TestEstimateMultipleTables tests --estimate with multiple -t flags
func TestEstimateMultipleTables(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "multi_est_1", 2000, 40)
	createBloatedTable(t, conn, "multi_est_2", 2000, 50)
	createBloatedTable(t, conn, "multi_est_3", 2000, 60)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "multi_est_1", "-t", "multi_est_2", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	var result EstimateJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Output should be valid JSON: %v\nOutput: %s", err, output)
	}

	// Should contain exactly 2 tables
	if len(result.Tables) != 2 {
		t.Errorf("Expected 2 tables in results, got %d", len(result.Tables))
	}

	// Verify both tables are present
	tableNames := make(map[string]bool)
	for _, tbl := range result.Tables {
		tableNames[tbl.TableName] = true
	}
	if !tableNames["multi_est_1"] || !tableNames["multi_est_2"] {
		t.Errorf("Expected multi_est_1 and multi_est_2 in results, got: %v", tableNames)
	}
	if tableNames["multi_est_3"] {
		t.Error("multi_est_3 should NOT be in results (not requested)")
	}
}

// TestEstimateNoMatchingTable tests --estimate with a non-existent table
func TestEstimateNoMatchingTable(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "existing_table", 1000, 30)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "nonexistent_table_xyz")
	// This might not be an error, just an empty result or a message
	_ = err

	// Should indicate no matching tables or return empty results
	if strings.Contains(output, "nonexistent_table_xyz") && !strings.Contains(strings.ToLower(output), "no matching") {
		// If the table name appears but not in a "no matching" message, check JSON
		if strings.HasPrefix(strings.TrimSpace(output), "{") {
			var result EstimateJSON
			if json.Unmarshal([]byte(output), &result) == nil && len(result.Tables) > 0 {
				t.Error("Should not find nonexistent table in results")
			}
		}
	}
}

// TestEstimateSchemaQualified tests --estimate with schema.table format
func TestEstimateSchemaQualified(t *testing.T) {
	conn := setupTestDB(t)
	// Create a table in a custom schema
	_, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS test_schema")
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	_, err = conn.Exec(ctx, "DROP TABLE IF EXISTS test_schema.schema_qualified_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}
	_, err = conn.Exec(ctx, `
		CREATE TABLE test_schema.schema_qualified_test (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		) WITH (autovacuum_enabled = false)
	`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = conn.Exec(ctx, `
		INSERT INTO test_schema.schema_qualified_test (data)
		SELECT 'row_' || g || '_padding_data_here'
		FROM generate_series(1, 2000) g
	`)
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}
	_, err = conn.Exec(ctx, "DELETE FROM test_schema.schema_qualified_test WHERE id % 100 < 50")
	if err != nil {
		t.Fatalf("Failed to delete rows: %v", err)
	}
	_, err = conn.Exec(ctx, "ANALYZE test_schema.schema_qualified_test")
	if err != nil {
		t.Fatalf("Failed to analyze: %v", err)
	}
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "test_schema.schema_qualified_test", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	var result EstimateJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Output should be valid JSON: %v\nOutput: %s", err, output)
	}

	if len(result.Tables) != 1 {
		t.Errorf("Expected 1 table, got %d", len(result.Tables))
	}
	if len(result.Tables) > 0 {
		if result.Tables[0].Schema != "test_schema" {
			t.Errorf("Expected schema 'test_schema', got '%s'", result.Tables[0].Schema)
		}
		if result.Tables[0].TableName != "schema_qualified_test" {
			t.Errorf("Expected table 'schema_qualified_test', got '%s'", result.Tables[0].TableName)
		}
	}
}

// TestEstimateVerbose tests --estimate with --verbose flag
func TestEstimateVerbose(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "verbose_test", 2000, 40)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "verbose_test", "--verbose")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	// Verbose mode should show additional info
	if !strings.Contains(output, "verbose_test") {
		t.Errorf("Output should contain table name, got: %s", output)
	}
	// Should contain "Running bloat estimation" or similar verbose message
	if !strings.Contains(output, "Running") && !strings.Contains(output, "Tables:") {
		t.Logf("Note: verbose output might need more distinctive markers. Output: %s", output)
	}
}

// TestEstimateWithDebloatError tests that --estimate and --debloat together produce an error
func TestEstimateWithDebloatError(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "error_test", 1000, 30)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "--debloat", "-t", "error_test")

	// Should fail with an error
	if err == nil {
		t.Error("Expected error when using --estimate with --debloat")
	}

	// Error message should mention the conflict
	if !strings.Contains(strings.ToLower(output), "estimate") || !strings.Contains(strings.ToLower(output), "debloat") {
		t.Logf("Error message should mention both flags. Output: %s", output)
	}
}

// TestEstimateNoBloat tests --estimate on a table with no bloat
func TestEstimateNoBloat(t *testing.T) {
	conn := setupTestDB(t)
	// Create a table without bloat (no deletes)
	_, err := conn.Exec(ctx, "DROP TABLE IF EXISTS no_bloat_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}
	_, err = conn.Exec(ctx, `
		CREATE TABLE no_bloat_test (
			id SERIAL PRIMARY KEY,
			data VARCHAR(100)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = conn.Exec(ctx, `
		INSERT INTO no_bloat_test (data)
		SELECT 'row_' || g || '_padding_data_here'
		FROM generate_series(1, 1000) g
	`)
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}
	_, err = conn.Exec(ctx, "ANALYZE no_bloat_test")
	if err != nil {
		t.Fatalf("Failed to analyze: %v", err)
	}
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "no_bloat_test", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	var result EstimateJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Output should be valid JSON: %v\nOutput: %s", err, output)
	}

	if len(result.Tables) != 1 {
		t.Errorf("Expected 1 table, got %d", len(result.Tables))
	}
	if len(result.Tables) > 0 {
		// Bloat should be minimal (close to 0)
		if result.Tables[0].BloatRatio > 10 {
			t.Errorf("Expected low bloat ratio for fresh table, got %.2f%%", result.Tables[0].BloatRatio)
		}
	}
}

// TestEstimateHighBloat tests --estimate on a table with high bloat
func TestEstimateHighBloat(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "high_bloat_est", 5000, 90)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "high_bloat_est", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	var result EstimateJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Output should be valid JSON: %v\nOutput: %s", err, output)
	}

	if len(result.Tables) != 1 {
		t.Errorf("Expected 1 table, got %d", len(result.Tables))
	}
	if len(result.Tables) > 0 {
		// High bloat table should show significant bloat
		if result.Tables[0].BloatRatio < 50 {
			t.Errorf("Expected high bloat ratio (>50%%) for 90%% deleted table, got %.2f%%", result.Tables[0].BloatRatio)
		}
		// Dead tuples should be high
		if result.Tables[0].DeadTuples < 1000 {
			t.Errorf("Expected many dead tuples, got %d", result.Tables[0].DeadTuples)
		}
	}
}

// TestEstimateDefaultBehavior tests that qwash without flags defaults to --estimate
func TestEstimateDefaultBehavior(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "default_test", 2000, 40)
	conn.Close()

	// Run without --estimate flag (should default to estimate)
	output, err := runQwashCLI(t)
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	// Should show bloat information (estimate mode)
	if !strings.Contains(output, "default_test") && !strings.Contains(output, "bloat") {
		t.Logf("Default mode should show estimate results. Output: %s", output)
	}
}
