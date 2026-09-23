package tests

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Alain-L/qwash/db"
)

// countBlockedAnalyze returns how many backends are waiting on a lock while
// running an ANALYZE of the given table.
func countBlockedAnalyze(t *testing.T, conn *db.DB, table string) int {
	var n int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE wait_event_type = 'Lock' AND query ILIKE 'ANALYZE %' || $1 || '%'
	`, table).Scan(&n); err != nil {
		t.Fatalf("Failed to query pg_stat_activity: %v", err)
	}
	return n
}

// TestCLIInterruptCancelsServerSide verifies that Ctrl-C cancels the statement
// running on the server and exits with code 130 (regression test: qwash only
// closed its socket, so the server went on running the statement with its
// locks held, and an interrupted run could exit 0).
func TestCLIInterruptCancelsServerSide(t *testing.T) {
	admin := setupTestDB(t)
	defer admin.Close()
	createBloatedTable(t, admin, "cancel_srv", 1000, 50)

	// Hold a lock that conflicts with ANALYZE, so qwash's first statement
	// blocks on the server until it is canceled.
	locker, err := db.Connect(getTestConfig(), false)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer locker.Close()
	if _, err := locker.Exec(ctx, "BEGIN; LOCK TABLE cancel_srv IN SHARE UPDATE EXCLUSIVE MODE"); err != nil {
		t.Fatalf("Failed to lock table: %v", err)
	}
	defer locker.Exec(ctx, "ROLLBACK")

	cfg := getTestConfig()
	cmd := exec.Command("./bin/qwash", "-h", cfg.Host, "-p", cfg.Port, "-U", cfg.User,
		"-d", cfg.Database, "--sslmode", cfg.SSLMode, "--debloat", "-t", "cancel_srv")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start qwash: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for countBlockedAnalyze(t, admin, "cancel_srv") == 0 {
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("qwash never blocked on the locked table")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("Failed to send SIGINT: %v", err)
	}
	err = cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
		t.Errorf("Expected exit code 130 after Ctrl-C, got %v", err)
	}

	// The server must have canceled the ANALYZE, not kept waiting for the lock.
	deadline = time.Now().Add(5 * time.Second)
	for countBlockedAnalyze(t, admin, "cancel_srv") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("ANALYZE still running on the server after Ctrl-C")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
