package db

import (
	"context"
	"fmt"
	"log"
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
	Verbose bool
}

// Connect establishes a connection to PostgreSQL and returns a DB struct
func Connect(cfg Config, verbose bool) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connString := fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database, cfg.SSLMode)

	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		log.Printf("[ERROR] Unable to connect to database: %v", err)
		return nil, err
	}

	if verbose {
		log.Printf("[INFO] Successfully connected to %s at %s:%s", cfg.Database, cfg.Host, cfg.Port)
	}

	return &DB{conn: conn, Verbose: verbose}, nil
}

// Close properly closes the database connection
func (db *DB) Close() {
	if db.conn != nil {
		db.conn.Close(context.Background())
		if db.Verbose {
			log.Println("[INFO] Database connection closed.")
		}
	}
}
