package analysis

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Alain-L/qwash/db"
)

// toastBloatQuery is the main query without the helper function creation.
// The helper function is created separately and dropped after use.
const toastBloatQuery = `
WITH bs AS (
  SELECT current_setting('block_size')::int AS block_size
),

toast_stats AS (
  SELECT
    ns.nspname AS schemaname,
    main.relname AS table_name,
    main.oid AS main_oid,
    toast.relname AS toast_relname,
    toast.relpages AS toast_pages,
    toast.reltuples::bigint AS toast_chunks,
    (toast.relpages * bs.block_size)::bigint AS toast_bytes,
    -- Stale when significant DML happened since the last VACUUM: the toast
    -- table's reltuples/relpages (used below) then still reflect the pre-DML
    -- state, so the estimate understates the real, reclaimable bloat. Use the
    -- dead-tuple ratio of the main table (>5%) rather than the vacuum age,
    -- which never fired right after a DELETE.
    COALESCE(st.n_dead_tup, 0) > 0
      AND COALESCE(st.n_dead_tup, 0)::float8
          / NULLIF(COALESCE(st.n_live_tup, 0) + COALESCE(st.n_dead_tup, 0), 0) > 0.05
      AS stale_stats
  FROM pg_class main
  JOIN pg_namespace ns ON ns.oid = main.relnamespace
  JOIN pg_class toast ON toast.oid = main.reltoastrelid
  LEFT JOIN pg_stat_user_tables st
    ON st.relid = main.oid
  CROSS JOIN bs
  WHERE main.relkind IN ('r', 'm')
    AND ns.nspname NOT IN ('pg_catalog', 'information_schema')
    AND toast.relpages > 0
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
    stale_stats,
    toast_pages::numeric / NULLIF(toast_chunks, 0) AS ppc,
    -- Raw average chunk size. The helper returns -1 when it could not read
    -- pg_toast (insufficient privilege), NULL when the toast table is empty.
    pg_temp._qwash_sample_chunk_size(main_oid) AS avg_chunk_raw,
    toast_pages >= 10 * 1024 * 1024 / (SELECT block_size FROM bs) AS is_reliable
  FROM toast_stats
),

ppc_ref_calc AS (
  SELECT
    bloat_calc.*,
    CASE WHEN avg_chunk_raw >= 0
         THEN (avg_chunk_raw + 50)::numeric / (SELECT block_size FROM bs)
         ELSE NULL
    END AS ppc_ref
  FROM bloat_calc
)

SELECT
  schemaname || '.' || table_name AS table_name,
  toast_bytes,
  toast_pages,
  toast_chunks,
  CASE
    WHEN NOT is_reliable OR ppc_ref IS NULL THEN NULL
    ELSE ROUND(GREATEST(0, (1 - ppc_ref / ppc) * 100)::numeric, 1)
  END AS bloat_pct,
  CASE
    WHEN NOT is_reliable OR ppc_ref IS NULL THEN NULL
    ELSE (GREATEST(0, (1 - ppc_ref / ppc)) * toast_bytes)::bigint
  END AS bloat_size,
  CASE
    WHEN NOT is_reliable THEN '< 10 MB'
    WHEN avg_chunk_raw = -1 THEN 'insufficient privilege'
    WHEN avg_chunk_raw IS NULL THEN 'no chunks'
    ELSE NULL
  END AS warning,
  stale_stats
FROM ppc_ref_calc
ORDER BY bloat_pct DESC NULLS LAST, toast_bytes DESC
`

// createHelperFunctionSQL creates a session-scoped temporary function to compute
// average chunk size. Using pg_temp schema ensures automatic cleanup on disconnect.
//
// Strategy: sequential scan computing avg(length(chunk_data)) across all live chunks.
// This naturally handles multi-column tables and mixed payload sizes — each chunk
// contributes proportionally to the average.
//
// Performance: ~90 ms per 100 MB of TOAST data (length() only reads the varlena
// header, so cost is dominated by sequential I/O, not detoasting).
const createHelperFunctionSQL = `
CREATE OR REPLACE FUNCTION pg_temp._qwash_sample_chunk_size(main_table_oid oid)
RETURNS integer AS $$
DECLARE
  chunk_size integer;
BEGIN
  EXECUTE format(
    'SELECT avg(length(chunk_data))::integer FROM pg_toast.pg_toast_%s',
    main_table_oid
  ) INTO chunk_size;

  RETURN chunk_size;
EXCEPTION
  WHEN insufficient_privilege THEN
    -- The caller cannot read pg_toast (not the table owner / not superuser).
    -- Signal it distinctly so we don't report a misleading "no chunks".
    RETURN -1;
  WHEN OTHERS THEN
    RETURN NULL;
END;
$$ LANGUAGE plpgsql
`

// dropHelperFunctionSQL removes the temporary function.
// Redundant with pg_temp auto-cleanup but kept for explicit cleanup within the session.
const dropHelperFunctionSQL = `DROP FUNCTION IF EXISTS pg_temp._qwash_sample_chunk_size(oid)`

// DetectToastBloat analyzes TOAST table bloat using the ppc algorithm.
// Requires recent VACUUM (not just ANALYZE) for accurate pg_class stats.
// Bloat estimation is only reliable for TOAST tables >= 10 MB.
func DetectToastBloat(ctx context.Context, dbConn *db.DB) ([]ToastBloat, error) {
	slog.Info("Analyzing TOAST bloat...")

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
			staleStats  bool
		)

		err := rows.Scan(
			&tableName,
			&toastBytes,
			&toastPages,
			&toastChunks,
			&bloatPct,
			&bloatSize,
			&warning,
			&staleStats,
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
			StaleStats:  staleStats,
		}

		if bloatSize != nil {
			tb.BloatSize = *bloatSize
		}
		if warning != nil {
			tb.Warning = *warning
		}

		results = append(results, tb)
	}

	slog.Info("TOAST bloat analysis complete", "tables", len(results))

	return results, nil
}
