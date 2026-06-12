package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Config holds the PostgreSQL connection parameters that were explicitly
// provided on the command line. Empty fields are omitted from the connection
// string so that pgx falls back to the standard PostgreSQL client conventions:
// PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE, PGSSLMODE, ~/.pgpass,
// service files and libpq-like defaults (local socket, OS user, sslmode
// prefer, ...).
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	SSLMode  string
}

// DSN renders the explicitly-set parameters as a libpq key/value connection
// string. Values are quoted, so passwords or paths containing spaces, quotes
// or backslashes are passed through correctly.
func (cfg Config) DSN() string {
	var parts []string
	add := func(key, value string) {
		if value != "" {
			parts = append(parts, key+"="+quoteDSNValue(value))
		}
	}
	add("host", cfg.Host)
	add("port", cfg.Port)
	add("user", cfg.User)
	add("password", cfg.Password)
	add("dbname", cfg.Database)
	add("sslmode", cfg.SSLMode)
	return strings.Join(parts, " ")
}

// quoteDSNValue quotes a libpq key/value parameter value. Plain values are
// returned as-is; values containing spaces, quotes or backslashes are wrapped
// in single quotes with backslash escaping, as libpq expects.
func quoteDSNValue(v string) string {
	if v != "" && !strings.ContainsAny(v, ` '\`) {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// DB represents a database connection
type DB struct {
	conn    *pgx.Conn
	config  Config // Store config for creating worker connections
	Verbose bool
	// Progress tracking for multi-table operations
	CurrentTableIndex int
	TotalTables       int
	// SilentProgress suppresses progress output (for JSON mode)
	SilentProgress bool
	// WorkerID identifies the worker in parallel mode (0 = main/sequential)
	WorkerID int
	// DelayMs is the delay in milliseconds between operations (--slow mode)
	DelayMs int
	// serverVersion caches server_version_num (0 until first lookup)
	serverVersion int
}

// ServerVersionNum returns the server's server_version_num (e.g. 180004 for
// PostgreSQL 18.4), cached after the first lookup. Returns 0 if unavailable.
func (db *DB) ServerVersionNum() int {
	if db.serverVersion == 0 {
		var n int
		if err := db.conn.QueryRow(context.Background(),
			"SELECT current_setting('server_version_num')::int").Scan(&n); err == nil {
			db.serverVersion = n
		}
	}
	return db.serverVersion
}

// Connect establishes a connection to PostgreSQL and returns a DB struct
func Connect(cfg Config, verbose bool) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connConfig, err := pgx.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("invalid connection parameters: %w", err)
	}

	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		// Return the error to the caller, which is responsible for logging.
		// Logging here as well produced a duplicate error line.
		return nil, err
	}

	if verbose {
		cc := conn.Config()
		slog.Info("successfully connected",
			"database", cc.Database, "host", cc.Host, "port", cc.Port, "user", cc.User)
	}

	return &DB{conn: conn, config: cfg, Verbose: verbose}, nil
}

// ResolvedTarget describes the live connection as user@host:port/dbname,
// using the values actually resolved by pgx (flags, environment or defaults).
func (db *DB) ResolvedTarget() string {
	cc := db.conn.Config()
	return fmt.Sprintf("%s@%s:%d/%s", cc.User, cc.Host, cc.Port, cc.Database)
}

// NewWorkerConnection creates a new connection for a worker goroutine.
// Each worker needs its own connection due to session-level state.
func (db *DB) NewWorkerConnection(workerID int) (*DB, error) {
	worker, err := Connect(db.config, false) // Workers are not verbose individually
	if err != nil {
		return nil, err
	}
	worker.WorkerID = workerID
	worker.SilentProgress = true // Workers don't print progress directly
	return worker, nil
}

// GetConfig returns the connection configuration (useful for parallel mode)
func (db *DB) GetConfig() Config {
	return db.config
}

// Close properly closes the database connection
func (db *DB) Close() {
	if db.conn != nil {
		db.conn.Close(context.Background())
		if db.Verbose {
			slog.Info("database connection closed")
		}
	}
}
