package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// DebloatJSON represents the JSON output structure from qwash --debloat --json
type DebloatJSON struct {
	Summary struct {
		TablesProcessed   int    `json:"tables_processed"`
		TablesCompacted   int    `json:"tables_compacted"`
		Errors            int    `json:"errors,omitempty"`
		Mode              string `json:"mode"`
		TotalPagesRemoved int    `json:"total_pages_removed"`
		TotalBytesRemoved int64  `json:"total_bytes_removed"`
		DurationMs        int64  `json:"duration_ms,omitempty"`
		LimitReached      bool   `json:"limit_reached,omitempty"`
	} `json:"summary"`
	Results []struct {
		Table             string `json:"table"`
		InitialPages      int    `json:"initial_pages"`
		FinalPages        int    `json:"final_pages"`
		BloatRemovedPages int    `json:"bloat_removed_pages"`
		BloatRemovedBytes int64  `json:"bloat_removed_bytes"`
		DurationMs        int64  `json:"duration_ms,omitempty"`
		Reindexed         bool   `json:"reindexed,omitempty"`
		Error             string `json:"error,omitempty"`
		DryRun            bool   `json:"dry_run,omitempty"`
	} `json:"results"`
}

// loadGoldenFile loads and parses a golden JSON file
func loadGoldenFile(t *testing.T, filename string) DebloatJSON {
	path := filepath.Join("golden", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read golden file %s: %v", filename, err)
	}

	var golden DebloatJSON
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("Failed to parse golden file %s: %v", filename, err)
	}

	return golden
}

// parseJSONOutput parses the JSON output from qwash
func parseJSONOutput(t *testing.T, output string) DebloatJSON {
	var result DebloatJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput was: %s", err, output)
	}
	return result
}

// compareDebloatJSON compares actual output with golden file, ignoring duration fields
func compareDebloatJSON(t *testing.T, actual, golden DebloatJSON) {
	// Compare summary (excluding duration)
	if actual.Summary.TablesProcessed != golden.Summary.TablesProcessed {
		t.Errorf("Summary.TablesProcessed: got %d, want %d",
			actual.Summary.TablesProcessed, golden.Summary.TablesProcessed)
	}
	if actual.Summary.TablesCompacted != golden.Summary.TablesCompacted {
		t.Errorf("Summary.TablesCompacted: got %d, want %d",
			actual.Summary.TablesCompacted, golden.Summary.TablesCompacted)
	}
	if actual.Summary.Mode != golden.Summary.Mode {
		t.Errorf("Summary.Mode: got %q, want %q",
			actual.Summary.Mode, golden.Summary.Mode)
	}
	if actual.Summary.TotalPagesRemoved != golden.Summary.TotalPagesRemoved {
		t.Errorf("Summary.TotalPagesRemoved: got %d, want %d",
			actual.Summary.TotalPagesRemoved, golden.Summary.TotalPagesRemoved)
	}
	if actual.Summary.TotalBytesRemoved != golden.Summary.TotalBytesRemoved {
		t.Errorf("Summary.TotalBytesRemoved: got %d, want %d",
			actual.Summary.TotalBytesRemoved, golden.Summary.TotalBytesRemoved)
	}

	// Compare results count
	if len(actual.Results) != len(golden.Results) {
		t.Errorf("Results count: got %d, want %d",
			len(actual.Results), len(golden.Results))
		return
	}

	// Sort both arrays by table name for idempotent comparison
	sort.Slice(actual.Results, func(i, j int) bool {
		return actual.Results[i].Table < actual.Results[j].Table
	})
	sort.Slice(golden.Results, func(i, j int) bool {
		return golden.Results[i].Table < golden.Results[j].Table
	})

	// Compare each result by position (now order-independent due to sorting)
	for i := range golden.Results {
		a := actual.Results[i]
		g := golden.Results[i]

		if a.Table != g.Table {
			t.Errorf("Results[%d].Table: got %q, want %q", i, a.Table, g.Table)
		}
		if a.InitialPages != g.InitialPages {
			t.Errorf("Table %q: InitialPages: got %d, want %d", g.Table, a.InitialPages, g.InitialPages)
		}
		if a.FinalPages != g.FinalPages {
			t.Errorf("Table %q: FinalPages: got %d, want %d", g.Table, a.FinalPages, g.FinalPages)
		}
		if a.BloatRemovedPages != g.BloatRemovedPages {
			t.Errorf("Table %q: BloatRemovedPages: got %d, want %d", g.Table, a.BloatRemovedPages, g.BloatRemovedPages)
		}
		if a.BloatRemovedBytes != g.BloatRemovedBytes {
			t.Errorf("Table %q: BloatRemovedBytes: got %d, want %d", g.Table, a.BloatRemovedBytes, g.BloatRemovedBytes)
		}
	}
}

// =============================================================================
// GOLDEN FILE TESTS
// =============================================================================

// TestGoldenDebloatDefaultSingle tests JSON output for single table default mode
func TestGoldenDebloatDefaultSingle(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "golden_test", 5000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "-t", "golden_test", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseJSONOutput(t, output)
	golden := loadGoldenFile(t, "debloat_default_single.json")

	compareDebloatJSON(t, actual, golden)
}

// TestGoldenDebloatFastSingle tests JSON output for single table fast mode
func TestGoldenDebloatFastSingle(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "golden_test", 5000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--fast", "-t", "golden_test", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseJSONOutput(t, output)
	golden := loadGoldenFile(t, "debloat_fast_single.json")

	compareDebloatJSON(t, actual, golden)
}

// TestGoldenDebloatSlowSingle tests JSON output for single table slow mode
func TestGoldenDebloatSlowSingle(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "golden_slow", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "--slow", "--delay", "1", "-t", "golden_slow", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseJSONOutput(t, output)
	golden := loadGoldenFile(t, "debloat_slow_single.json")

	compareDebloatJSON(t, actual, golden)
}

// TestGoldenDebloatMultipleTables tests JSON output for multiple tables
func TestGoldenDebloatMultipleTables(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "multi_1", 3000, 30)
	createBloatedTable(t, conn, "multi_2", 3000, 50)
	createBloatedTable(t, conn, "multi_3", 3000, 70)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "-t", "multi_1", "-t", "multi_2", "-t", "multi_3", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseJSONOutput(t, output)
	golden := loadGoldenFile(t, "debloat_multiple_tables.json")

	compareDebloatJSON(t, actual, golden)
}

// TestGoldenDebloatHighBloat tests JSON output for high bloat table
func TestGoldenDebloatHighBloat(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "high_bloat", 5000, 90)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "-t", "high_bloat", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseJSONOutput(t, output)
	golden := loadGoldenFile(t, "debloat_high_bloat.json")

	compareDebloatJSON(t, actual, golden)
}

// TestGoldenDebloatLowBloat tests JSON output for low bloat table
func TestGoldenDebloatLowBloat(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "low_bloat", 5000, 10)
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "-t", "low_bloat", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseJSONOutput(t, output)
	golden := loadGoldenFile(t, "debloat_low_bloat.json")

	compareDebloatJSON(t, actual, golden)
}

// =============================================================================
// ESTIMATE GOLDEN FILE TYPES AND HELPERS
// =============================================================================

// EstimateGoldenJSON represents the JSON output structure from qwash --estimate --json
type EstimateGoldenJSON struct {
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
	Indexes interface{} `json:"indexes"`
}

// loadEstimateGoldenFile loads and parses an estimate golden JSON file
func loadEstimateGoldenFile(t *testing.T, filename string) EstimateGoldenJSON {
	path := filepath.Join("golden", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read golden file %s: %v", filename, err)
	}

	var golden EstimateGoldenJSON
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("Failed to parse golden file %s: %v", filename, err)
	}

	return golden
}

// parseEstimateJSONOutput parses the estimate JSON output from qwash
func parseEstimateJSONOutput(t *testing.T, output string) EstimateGoldenJSON {
	var result EstimateGoldenJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput was: %s", err, output)
	}
	return result
}

// compareEstimateJSON compares actual estimate output with golden file
// Note: live_tuples and dead_tuples are skipped because they come from
// pg_stat_user_tables which is updated asynchronously and may be stale
// in freshly created test databases
func compareEstimateJSON(t *testing.T, actual, golden EstimateGoldenJSON) {
	// Compare tables count
	if len(actual.Tables) != len(golden.Tables) {
		t.Errorf("Tables count: got %d, want %d",
			len(actual.Tables), len(golden.Tables))
		return
	}

	// Compare each table (excluding live_tuples/dead_tuples which depend on stats collector)
	for i := range golden.Tables {
		a := actual.Tables[i]
		g := golden.Tables[i]

		if a.Schema != g.Schema {
			t.Errorf("Tables[%d].Schema: got %q, want %q", i, a.Schema, g.Schema)
		}
		if a.TableName != g.TableName {
			t.Errorf("Tables[%d].TableName: got %q, want %q", i, a.TableName, g.TableName)
		}
		if a.TableSize != g.TableSize {
			t.Errorf("Tables[%d].TableSize: got %d, want %d", i, a.TableSize, g.TableSize)
		}
		if a.BloatSize != g.BloatSize {
			t.Errorf("Tables[%d].BloatSize: got %d, want %d", i, a.BloatSize, g.BloatSize)
		}
		if a.BloatRatio != g.BloatRatio {
			t.Errorf("Tables[%d].BloatRatio: got %.2f, want %.2f", i, a.BloatRatio, g.BloatRatio)
		}
		if a.Pages != g.Pages {
			t.Errorf("Tables[%d].Pages: got %d, want %d", i, a.Pages, g.Pages)
		}
		if a.MinPages != g.MinPages {
			t.Errorf("Tables[%d].MinPages: got %d, want %d", i, a.MinPages, g.MinPages)
		}
		// Skip live_tuples and dead_tuples comparison - these come from pg_stat_user_tables
		// which is updated asynchronously and may show 0 in freshly created databases
		if a.FillFactor != g.FillFactor {
			t.Errorf("Tables[%d].FillFactor: got %d, want %d", i, a.FillFactor, g.FillFactor)
		}
	}
}

// =============================================================================
// ESTIMATE GOLDEN FILE TESTS
// =============================================================================

// TestGoldenEstimateSingle tests JSON output for single table estimate
func TestGoldenEstimateSingle(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "golden_est_single", 3000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "golden_est_single", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseEstimateJSONOutput(t, output)
	golden := loadEstimateGoldenFile(t, "estimate_single.json")

	compareEstimateJSON(t, actual, golden)
}

// TestGoldenEstimateMultiple tests JSON output for multiple tables estimate
func TestGoldenEstimateMultiple(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "golden_est_multi_1", 2000, 30)
	createBloatedTable(t, conn, "golden_est_multi_2", 2000, 60)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "golden_est_multi_1", "-t", "golden_est_multi_2", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseEstimateJSONOutput(t, output)
	golden := loadEstimateGoldenFile(t, "estimate_multiple.json")

	compareEstimateJSON(t, actual, golden)
}

// TestGoldenEstimateHighBloat tests JSON output for high bloat estimate
func TestGoldenEstimateHighBloat(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "golden_est_high", 5000, 90)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-t", "golden_est_high", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	actual := parseEstimateJSONOutput(t, output)
	golden := loadEstimateGoldenFile(t, "estimate_high_bloat.json")

	compareEstimateJSON(t, actual, golden)
}
