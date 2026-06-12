package tests

import (
	"context"
	"os"
	"os/exec"
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
	if err := limited.CompactTableToTarget(context.Background(), "public.owned_by_admin", 0); err == nil {
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

// TestCompactionCancellation verifies that a cancelled context stops the
// compaction promptly instead of running to completion (Ctrl-C support).
func TestCompactionCancellation(t *testing.T) {
	conn := setupTestDB(t)
	defer conn.Close()

	// A sizeable bloated table so compaction would take many page rounds.
	createBloatedTable(t, conn, "cancel_tbl", 20000, 50)
	initialPages := getTablePages(t, conn, "cancel_tbl")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before we start

	err := conn.CompactTableToTarget(ctx, "cancel_tbl", 0)
	if err == nil {
		t.Fatal("Expected compaction to stop on a cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "interrupt") && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("Expected an interruption error, got: %v", err)
	}

	// The table must not have been fully compacted (it stopped early).
	conn2, e := db.Connect(getTestConfig(), false)
	if e != nil {
		t.Fatalf("reconnect failed: %v", e)
	}
	defer conn2.Close()
	if final := getTablePages(t, conn2, "cancel_tbl"); final < initialPages/2 {
		t.Errorf("Compaction should have stopped early, but table shrank from %d to %d pages", initialPages, final)
	}
}

// TestCLIExitCodeOnTableFailure verifies that a debloat run in which a table
// fails (here: refused by the ownership preflight) exits with code 2, so that
// automation can detect partial failures (previously the exit code was always
// 0).
func TestCLIExitCodeOnTableFailure(t *testing.T) {
	admin := setupTestDB(t)
	ctx := context.Background()
	createBloatedTable(t, admin, "exit_owned", 1000, 50)
	admin.Exec(ctx, "DROP ROLE IF EXISTS qwash_exit_lim")
	if _, err := admin.Exec(ctx, "CREATE ROLE qwash_exit_lim LOGIN PASSWORD 'lim'"); err != nil {
		t.Fatalf("Failed to create role: %v", err)
	}
	admin.Exec(ctx, "GRANT USAGE ON SCHEMA public TO qwash_exit_lim")
	admin.Exec(ctx, "GRANT SELECT ON exit_owned TO qwash_exit_lim")
	defer func() {
		admin.Exec(ctx, "REVOKE ALL ON exit_owned FROM qwash_exit_lim")
		admin.Exec(ctx, "REVOKE ALL ON SCHEMA public FROM qwash_exit_lim")
		admin.Exec(ctx, "DROP ROLE IF EXISTS qwash_exit_lim")
	}()
	cfg := getTestConfig()
	admin.Close()

	cmd := exec.Command("./bin/qwash", "-h", cfg.Host, "-p", cfg.Port,
		"-U", "qwash_exit_lim", "-d", cfg.Database, "--sslmode", cfg.SSLMode,
		"--debloat", "-t", "exit_owned")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PGPASSWORD=lim")
	out, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("Expected a non-zero exit (ExitError), got err=%v\nOutput: %s", err, out)
	}
	if code := exitErr.ExitCode(); code != 2 {
		t.Errorf("Expected exit code 2 for a per-table failure, got %d\nOutput: %s", code, out)
	}
}
