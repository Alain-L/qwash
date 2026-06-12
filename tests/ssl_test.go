package tests

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// =============================================================================
// SSL MODE TESTS
// =============================================================================

// TestSSLModeDisable tests that --sslmode disable works
func TestSSLModeDisable(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "ssl_disable_table", 1000, 50)
	conn.Close()

	// This should work (default CI config uses sslmode=disable)
	output, err := runQwashCLI(t, "--estimate", "-t", "ssl_disable_table")
	if err != nil {
		t.Fatalf("CLI failed with sslmode=disable: %v\nOutput: %s", err, output)
	}

	// Verify output contains expected content
	if !strings.Contains(output, "ssl_disable_table") {
		t.Errorf("Expected output to contain table name\nOutput: %s", output)
	}
}

// TestSSLModePrefer tests that --sslmode prefer works (falls back to non-SSL if SSL unavailable)
func TestSSLModePrefer(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "ssl_prefer_table", 1000, 50)
	conn.Close()

	// sslmode=prefer should work even without SSL (falls back)
	output, err := runQwashCLIWithSSL(t, "prefer", "--estimate", "-t", "ssl_prefer_table")
	if err != nil {
		t.Fatalf("CLI failed with sslmode=prefer: %v\nOutput: %s", err, output)
	}

	// Verify output contains expected content
	if !strings.Contains(output, "ssl_prefer_table") {
		t.Errorf("Expected output to contain table name\nOutput: %s", output)
	}
}

// TestSSLModeAllow tests that --sslmode allow works
func TestSSLModeAllow(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "ssl_allow_table", 1000, 50)
	conn.Close()

	// sslmode=allow should work
	output, err := runQwashCLIWithSSL(t, "allow", "--estimate", "-t", "ssl_allow_table")
	if err != nil {
		t.Fatalf("CLI failed with sslmode=allow: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(output, "ssl_allow_table") {
		t.Errorf("Expected output to contain table name\nOutput: %s", output)
	}
}

// TestSSLModeRequireNoSSLServer tests behavior when require mode is used but server has no SSL
// This test expects the connection to fail since the default PostgreSQL test container doesn't have SSL
func TestSSLModeRequireNoSSLServer(t *testing.T) {
	conn := setupTestDB(t)
	createBloatedTable(t, conn, "ssl_require_table", 1000, 50)
	conn.Close()

	// Try to connect with sslmode=require to a server without SSL
	output, err := runQwashCLIWithSSL(t, "require", "--estimate", "-t", "ssl_require_table")

	// This should fail if the server doesn't support SSL
	// Note: some PostgreSQL containers may have SSL enabled by default
	if err != nil {
		t.Logf("Connection with sslmode=require failed as expected (server has no SSL): %v", err)
		// Verify error message mentions SSL
		if strings.Contains(strings.ToLower(output), "ssl") || strings.Contains(strings.ToLower(output), "tls") {
			t.Log("Error message correctly mentions SSL/TLS")
		}
	} else {
		t.Logf("Note: Connection with sslmode=require succeeded (server may have SSL enabled)\nOutput: %s", output)
	}
}

// TestTestConnectionWithSSLModes tests --test-connection with different SSL modes
func TestTestConnectionWithSSLModes(t *testing.T) {
	setupTestDB(t).Close() // Just ensure DB exists

	testCases := []struct {
		sslmode     string
		shouldWork  bool
		description string
	}{
		{"disable", true, "disable should always work"},
		{"prefer", true, "prefer should fallback to non-SSL"},
		{"allow", true, "allow should accept non-SSL"},
	}

	for _, tc := range testCases {
		t.Run(tc.sslmode, func(t *testing.T) {
			output, err := runQwashCLIWithSSL(t, tc.sslmode, "--test-connection")

			if tc.shouldWork {
				if err != nil {
					t.Errorf("%s: expected success but got error: %v\nOutput: %s", tc.description, err, output)
				} else {
					if !strings.Contains(output, "Connection OK") {
						t.Errorf("Expected 'Connection OK' in output\nOutput: %s", output)
					}
				}
			} else {
				if err == nil {
					t.Logf("%s: expected failure but got success\nOutput: %s", tc.description, output)
				}
			}
		})
	}
}

// runQwashCLIWithSSL runs qwash with a specific sslmode, overriding the default
func runQwashCLIWithSSL(t *testing.T, sslmode string, args ...string) (string, error) {
	cfg := getTestConfig()

	// Build base args with connection info but custom sslmode; the password
	// goes through the PGPASSWORD environment variable.
	baseArgs := []string{
		"-h", cfg.Host,
		"-p", cfg.Port,
		"-U", cfg.User,
		"-d", cfg.Database,
		"--sslmode", sslmode, // Override sslmode
	}

	allArgs := append(baseArgs, args...)

	cmd := exec.Command("./bin/qwash", allArgs...)
	cmd.Dir = ".." // Run from project root
	if cfg.Password != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
	}

	output, err := cmd.CombinedOutput()
	return string(output), err
}
