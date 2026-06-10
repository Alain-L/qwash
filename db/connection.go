package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Config holds PostgreSQL connection parameters
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	SSLMode  string
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
}

// Connect establishes a connection to PostgreSQL and returns a DB struct
func Connect(cfg Config, verbose bool) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connString := fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database, cfg.SSLMode)

	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		// Return the error to the caller, which is responsible for logging.
		// Logging here as well produced a duplicate error line.
		return nil, err
	}

	if verbose {
		slog.Info("successfully connected", "database", cfg.Database, "host", cfg.Host, "port", cfg.Port)
	}

	return &DB{conn: conn, config: cfg, Verbose: verbose}, nil
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
