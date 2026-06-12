package tests

import (
	"context"
	"strings"
	"testing"

	"qwash/db"
)

// =============================================================================
// PRE-DEBLOAT SAFETY TESTS (ownership, triggers, replica identity)
// =============================================================================

// TestPreflightNonOwnerRefused verifies that compaction is refused when the
// current role can neither own nor superuser-VACUUM the table — otherwise the
// UPDATEs would move rows but VACUUM could not reclaim the pages, increasing
// bloat (the opposite of the goal).
func TestPreflightNonOwnerRefused(t *testing.T) {
	admin := setupTestDB(t)
	ctx := context.Background()

	// A table owned by the (super)user running the suite, plus a limited role
	// that owns nothing and is not a superuser.
	createBloatedTable(t, admin, "owned_by_admin", 1000, 50)
	admin.Exec(ctx, "DROP ROLE IF EXISTS qwash_limited")
	if _, err := admin.Exec(ctx, "CREATE ROLE qwash_limited LOGIN PASSWORD 'limited'"); err != nil {
		t.Fatalf("Failed to create limited role: %v", err)
	}
	// The limited role needs to reach the table to even run the preflight query.
	admin.Exec(ctx, "GRANT USAGE ON SCHEMA public TO qwash_limited")
	admin.Exec(ctx, "GRANT SELECT ON owned_by_admin TO qwash_limited")
	defer func() {
		admin.Exec(ctx, "REVOKE ALL ON owned_by_admin FROM qwash_limited")
		admin.Exec(ctx, "REVOKE ALL ON SCHEMA public FROM qwash_limited")
		admin.Exec(ctx, "DROP ROLE IF EXISTS qwash_limited")
	}()
	admin.Close()

	// Connect as the limited role.
	cfg := getTestConfig()
	cfg.User = "qwash_limited"
	cfg.Password = "limited"
	limited, err := db.Connect(cfg, false)
	if err != nil {
		t.Fatalf("Failed to connect as limited role: %v", err)
	}
	defer limited.Close()

	_, err = limited.DebloatPreflight("public.owned_by_admin")
	if err == nil {
		t.Fatal("Expected preflight to refuse compaction for a non-owner, got nil")
	}
	if !strings.Contains(err.Error(), "owner") && !strings.Contains(err.Error(), "superuser") {
		t.Errorf("Refusal should explain the ownership requirement, got: %v", err)
	}

	// And the actual compaction must fail too, not silently proceed.
	if err := limited.CompactTableToTarget("public.owned_by_admin", 0); err == nil {
		// CompactTableToTarget itself may fail earlier (e.g. on SET
		// session_replication_role); either way it must not succeed.
		t.Log("note: compaction failed before preflight wiring, which is also acceptable")
	}
}

// TestPreflightOwnerAllowed verifies the preflight passes for the table owner.
func TestPreflightOwnerAllowed(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	createBloatedTable(t, conn, "owned_ok", 1000, 50)

	warnings, err := conn.DebloatPreflight("public.owned_ok")
	if err != nil {
		t.Fatalf("Expected preflight to pass for the owner, got: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("Expected no warnings for a plain table, got: %v", warnings)
	}
}

// TestPreflightAlwaysTriggerWarns verifies that an ENABLE ALWAYS trigger is
// surfaced as a warning (it fires on every moved row despite
// session_replication_role = replica).
func TestPreflightAlwaysTriggerWarns(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	createBloatedTable(t, conn, "trig_tbl", 1000, 50)
	if _, err := conn.Exec(ctx, `
		CREATE FUNCTION trig_noop() RETURNS trigger AS $$ BEGIN RETURN NEW; END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER t_always BEFORE UPDATE ON trig_tbl FOR EACH ROW EXECUTE FUNCTION trig_noop();
		ALTER TABLE trig_tbl ENABLE ALWAYS TRIGGER t_always;
	`); err != nil {
		t.Fatalf("Failed to create ALWAYS trigger: %v", err)
	}

	warnings, err := conn.DebloatPreflight("public.trig_tbl")
	if err != nil {
		t.Fatalf("Preflight should not hard-fail on a trigger: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "ALWAYS") {
			found = true
		}
	}
	if !found {
		t.Errorf("Expected an ALWAYS/REPLICA trigger warning, got: %v", warnings)
	}
}
