package analysis

import (
	"context"
	"fmt"
	"log"
	"strings"

	"qwash/db"
)

// toastBloatQuery is the main query without the helper function creation.
// The helper function is created separately and dropped after use.
const toastBloatQuery = `
WITH toast_stats AS (
  SELECT
    ns.nspname AS schemaname,
    main.relname AS table_name,
    main.oid AS main_oid,
    toast.relname AS toast_relname,
    toast.relpages AS toast_pages,
    toast.reltuples::bigint AS toast_chunks,
    (toast.relpages * 8192)::bigint AS toast_bytes
  FROM pg_class main
  JOIN pg_namespace ns ON ns.oid = main.relnamespace
  JOIN pg_class toast ON toast.oid = main.reltoastrelid
  WHERE main.relkind IN ('r', 'm')
    AND ns.nspname NOT IN ('pg_catalog', 'information_schema')
),

bloat_calc AS (
  SELECT
    schemaname,
    table_name,
    main_oid,
    toast_relname,
    toast_pages,
    toast_chunks,
    toast_bytes,
    toast_pages::numeric / NULLIF(toast_chunks, 0) AS ppc,
    (_qwash_sample_chunk_size(main_oid) + 50)::numeric / 8192 AS ppc_ref,
    toast_pages >= 1250 AS is_reliable
  FROM toast_stats
)

SELECT
  schemaname || '.' || table_name AS table_name,
  toast_bytes,
  toast_pages,
  toast_chunks,
  CASE
    WHEN NOT is_reliable THEN NULL
    WHEN ppc_ref IS NULL THEN NULL
    ELSE ROUND(GREATEST(0, (1 - ppc_ref / ppc) * 100)::numeric, 1)
  END AS bloat_pct,
  CASE
    WHEN NOT is_reliable THEN NULL
    WHEN ppc_ref IS NULL THEN NULL
    ELSE (GREATEST(0, (1 - ppc_ref / ppc)) * toast_bytes)::bigint
  END AS bloat_size,
  CASE
    WHEN NOT is_reliable THEN '< 10 MB'
    WHEN ppc_ref IS NULL THEN 'no chunks'
    ELSE NULL
  END AS warning
FROM bloat_calc
ORDER BY bloat_pct DESC NULLS LAST, toast_bytes DESC
`

// createHelperFunctionSQL creates a temporary function to sample chunk size
const createHelperFunctionSQL = `
CREATE OR REPLACE FUNCTION _qwash_sample_chunk_size(main_table_oid oid)
RETURNS integer AS $$
DECLARE
  chunk_size integer;
BEGIN
  EXECUTE format(
    'SELECT length(chunk_data) FROM pg_toast.pg_toast_%s LIMIT 1',
    main_table_oid
  ) INTO chunk_size;
  RETURN chunk_size;
EXCEPTION WHEN OTHERS THEN
  RETURN NULL;
END;
$$ LANGUAGE plpgsql
`

// dropHelperFunctionSQL removes the temporary function
const dropHelperFunctionSQL = `DROP FUNCTION IF EXISTS _qwash_sample_chunk_size(oid)`

// DetectToastBloat analyzes TOAST table bloat using the ppc algorithm.
// Requires recent VACUUM (not just ANALYZE) for accurate pg_class stats.
// Bloat estimation is only reliable for TOAST tables >= 10 MB.
func DetectToastBloat(ctx context.Context, dbConn *db.DB) ([]ToastBloat, error) {
	if dbConn.Verbose {
		log.Println("[INFO] Analyzing TOAST bloat...")
	}

	// Create helper function
	_, err := dbConn.Exec(ctx, createHelperFunctionSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to create helper function: %w", err)
	}

	// Ensure cleanup
	defer func() {
		_, _ = dbConn.Exec(ctx, dropHelperFunctionSQL)
	}()

	// Execute main query
	rows, err := dbConn.Query(ctx, toastBloatQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch TOAST bloat data: %w", err)
	}
	defer rows.Close()

	var results []ToastBloat

	for rows.Next() {
		var (
			tableName   string
			toastBytes  int64
			toastPages  int
			toastChunks int64
			bloatPct    *float64
			bloatSize   *int64
			warning     *string
		)

		err := rows.Scan(
			&tableName,
			&toastBytes,
			&toastPages,
			&toastChunks,
			&bloatPct,
			&bloatSize,
			&warning,
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

		tb := ToastBloat{
			Schema:      schema,
			TableName:   table,
			ToastSize:   toastBytes,
			ToastPages:  toastPages,
			ToastChunks: toastChunks,
			BloatPct:    bloatPct,
		}

		if bloatSize != nil {
			tb.BloatSize = *bloatSize
		}
		if warning != nil {
			tb.Warning = *warning
		}

		results = append(results, tb)
	}

	if dbConn.Verbose {
		log.Printf("[INFO] Found %d tables with TOAST data.\n", len(results))
	}

	return results, nil
}
