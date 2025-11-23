package analysis

import (
	"context"
	"fmt"
	"log"
	"strings"

	"qwash/db"
	"qwash/sql"
)

// DetectTableBloat analyzes table bloat in PostgreSQL using the embedded bloat query.
func DetectTableBloat(ctx context.Context, dbConn *db.DB) ([]BloatTable, error) {
	log.Println("[INFO] Analyzing table bloat...")

	rows, err := dbConn.Query(ctx, sql.TableBloatSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch table bloat data: %w", err)
	}
	defer rows.Close()

	var bloatTables []BloatTable

	for rows.Next() {
		var (
			tableName       string
			liveTup         int
			deadTup         int64
			minPages        int
			actualPages     int
			fillfactor      int
			relationSize    string // pg_size_pretty output
			toastSize       string
			bloatSizeStr    string
			bloatPct        float64
		)

		err := rows.Scan(
			&tableName,
			&liveTup,
			&deadTup,
			&minPages,
			&actualPages,
			&fillfactor,
			&relationSize,
			&toastSize,
			&bloatSizeStr,
			&bloatPct,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %w", err)
		}

		// Parse table_name format "schema.table"
		parts := strings.Split(tableName, ".")
		var schema, table string
		if len(parts) == 2 {
			schema = parts[0]
			table = parts[1]
		} else {
			schema = "public"
			table = tableName
		}

		// Convert pg_size_pretty output to bytes (approximate for now)
		bloatSize := parseSizeToBytes(bloatSizeStr)
		tableSize := parseSizeToBytes(relationSize)

		tbl := BloatTable{
			Schema:     schema,
			TableName:  table,
			TableSize:  tableSize,
			BloatSize:  bloatSize,
			BloatRatio: bloatPct,
		}

		bloatTables = append(bloatTables, tbl)
	}

	log.Printf("[INFO] Found %d tables with bloat information.\n", len(bloatTables))
	return bloatTables, nil
}

// parseSizeToBytes converts PostgreSQL pg_size_pretty output to bytes (approximate)
func parseSizeToBytes(sizeStr string) int64 {
	sizeStr = strings.TrimSpace(sizeStr)

	// Handle "N/A" or "0 bytes"
	if sizeStr == "N/A" || sizeStr == "0 bytes" {
		return 0
	}

	var value float64
	var unit string
	fmt.Sscanf(sizeStr, "%f %s", &value, &unit)

	switch strings.ToLower(unit) {
	case "bytes":
		return int64(value)
	case "kb":
		return int64(value * 1024)
	case "mb":
		return int64(value * 1024 * 1024)
	case "gb":
		return int64(value * 1024 * 1024 * 1024)
	case "tb":
		return int64(value * 1024 * 1024 * 1024 * 1024)
	default:
		return 0
	}
}
