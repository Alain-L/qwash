package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/Alain-L/qwash/db"
)

// =============================================================================
// TABLE TARGETING TESTS (schema qualification, homonym tables, filters)
// =============================================================================

// TestResolveTableName verifies the canonical schema-qualified resolution of
// table names, including homonym tables in several schemas (regression test:
// the bloat estimation used to match a table by bare name across all schemas
// while the DML resolved it through the search_path, so the two could
// silently designate different relations).
func TestResolveTableName(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	// public.homonym_tbl: lightly bloated; side.homonym_tbl: heavily bloated
	createBloatedTable(t, conn, "homonym_tbl", 1000, 10)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA side"); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	createBloatedTable(t, conn, "side.homonym_tbl", 5000, 70)

	// Bare name resolves through the search_path, like the DML would
	resolved, err := conn.ResolveTableName("homonym_tbl")
	if err != nil {
		t.Fatalf("ResolveTableName failed: %v", err)
	}
	if resolved != "public.homonym_tbl" {
		t.Errorf("Expected bare name to resolve to public.homonym_tbl, got %q", resolved)
	}

	// Qualified names resolve to themselves
	resolved, err = conn.ResolveTableName("side.homonym_tbl")
	if err != nil {
		t.Fatalf("ResolveTableName failed: %v", err)
	}
	if resolved != "side.homonym_tbl" {
		t.Errorf("Expected qualified name to resolve to side.homonym_tbl, got %q", resolved)
	}

	// Nonexistent tables fail fast with a clear error
	if _, err := conn.ResolveTableName("does_not_exist_xyz"); err == nil {
		t.Error("Expected an error for a nonexistent table")
	}

	// The qualified estimations must differ: side.homonym_tbl is far more
	// bloated than public.homonym_tbl
	pubBloat := getBloatPages(t, conn, "public.homonym_tbl")
	sideBloat := getBloatPages(t, conn, "side.homonym_tbl")
	if sideBloat <= pubBloat {
		t.Errorf("Expected side.homonym_tbl (%d bloat pages) to be more bloated than public.homonym_tbl (%d)",
			sideBloat, pubBloat)
	}
}

// TestCLIDebloatHomonymTable verifies that debloating a bare table name only
// touches the search_path relation, leaving homonym tables in other schemas
// untouched.
func TestCLIDebloatHomonymTable(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "homonym_cli", 5000, 50)
	if _, err := conn.Exec(context.Background(), "CREATE SCHEMA side"); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	createBloatedTable(t, conn, "side.homonym_cli", 5000, 70)

	initialPublic := getTablePages(t, conn, "public.homonym_cli")
	initialSide := getTablePages(t, conn, "side.homonym_cli")
	conn.Close()

	output, err := runQwashCLI(t, "--debloat", "-t", "homonym_cli")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	conn2, err := db.Connect(getTestConfig(), false)
	if err != nil {
		t.Fatalf("Failed to reconnect: %v", err)
	}
	defer conn2.Close()

	if finalPublic := getTablePages(t, conn2, "public.homonym_cli"); finalPublic >= initialPublic {
		t.Errorf("Expected page reduction on public.homonym_cli: initial=%d, final=%d",
			initialPublic, finalPublic)
	}
	if finalSide := getTablePages(t, conn2, "side.homonym_cli"); finalSide != initialSide {
		t.Errorf("side.homonym_cli must not be touched: initial=%d, final=%d",
			initialSide, finalSide)
	}
}

// TestCLIDebloatNonexistentTable verifies that --debloat -t with an unknown
// table fails fast instead of silently succeeding.
func TestCLIDebloatNonexistentTable(t *testing.T) {
	setupTestDB(t).Close()

	output, err := runQwashCLI(t, "--debloat", "-t", "does_not_exist_xyz")
	if err == nil {
		t.Errorf("Expected error for nonexistent table, got success\nOutput: %s", output)
	}
	if !strings.Contains(output, "does_not_exist_xyz") {
		t.Errorf("Error message should name the missing table\nOutput: %s", output)
	}
}

// TestCLIEstimateSchemaFilter verifies that --schema restricts the estimate
// report (regression test: -n used to be silently ignored in estimate mode).
func TestCLIEstimateSchemaFilter(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "filter_main", 1000, 50)
	if _, err := conn.Exec(context.Background(), "CREATE SCHEMA xtra"); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	createBloatedTable(t, conn, "xtra.filter_side", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-n", "xtra", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(output, "filter_side") {
		t.Errorf("Expected xtra.filter_side in schema-filtered report\nOutput: %s", output)
	}
	if strings.Contains(output, "filter_main") {
		t.Errorf("public.filter_main must not appear when filtering on schema xtra\nOutput: %s", output)
	}
}

// TestCLIEstimateExcludeTable verifies that --exclude-table removes tables
// from the estimate report (regression test: -X used to be silently ignored
// in estimate mode).
func TestCLIEstimateExcludeTable(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "filter_kept", 1000, 50)
	createBloatedTable(t, conn, "filter_dropped", 1000, 50)
	conn.Close()

	output, err := runQwashCLI(t, "--estimate", "-X", "filter_dropped", "--json")
	if err != nil {
		t.Fatalf("CLI failed: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(output, "filter_kept") {
		t.Errorf("Expected filter_kept in report\nOutput: %s", output)
	}
	if strings.Contains(output, "filter_dropped") {
		t.Errorf("filter_dropped must not appear when excluded\nOutput: %s", output)
	}
}
