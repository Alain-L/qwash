package analysis

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Alain-L/qwash/db"
)

// btreeBloatQuery estimates B-Tree index bloat using catalog statistics.
// Based on the ioguix btree bloat estimation approach: compares theoretical
// minimum pages (from tuple count, key width via pg_stats, fillfactor, and
// B-Tree page overhead) with actual pages.
//
// Warning: columns of type "name" produce unreliable pg_stats (is_na = true).
// Compatible with PostgreSQL 9.6+.
const btreeBloatQuery = `
WITH constants AS (
  SELECT
    current_setting('block_size')::int AS block_size,
    CASE WHEN version() ~ '64-bit|x86_64|ppc64|ia64|amd64' THEN 8
         ELSE 4
    END AS maxalign,
    24 AS page_header,
    16 AS page_opaque,
    4 AS item_pointer,
    8 AS index_tuple_hdr
),

index_attrs AS (
  SELECT
    ic.indexrelid,
    ic.indrelid,
    ci.relname AS index_name,
    ci.reltuples,
    ci.relpages,
    ci.relnamespace,
    COALESCE(
      substring(array_to_string(ci.reloptions, ' ')
        FROM 'fillfactor=([0-9]+)')::smallint,
      90
    ) AS fillfactor,
    COALESCE(a1.attnum, a2.attnum) AS attnum,
    COALESCE(a1.attname, a2.attname) AS attname,
    COALESCE(a1.atttypid, a2.atttypid) AS atttypid,
    CASE WHEN a1.attnum IS NULL THEN ci.relname
         ELSE ct.relname
    END AS stats_relname
  FROM pg_index ic
  JOIN pg_class ci ON ci.oid = ic.indexrelid
  JOIN pg_class ct ON ct.oid = ic.indrelid
  CROSS JOIN LATERAL generate_series(1, ic.indnatts) AS pos(col)
  CROSS JOIN LATERAL (
    SELECT pg_catalog.string_to_array(
      pg_catalog.textin(pg_catalog.int2vectorout(ic.indkey)), ' '
    )::int[] AS arr
  ) AS indkey_arr
  LEFT JOIN pg_attribute a1
    ON indkey_arr.arr[pos.col] <> 0
    AND a1.attrelid = ic.indrelid
    AND a1.attnum = indkey_arr.arr[pos.col]
  LEFT JOIN pg_attribute a2
    ON indkey_arr.arr[pos.col] = 0
    AND a2.attrelid = ic.indexrelid
    AND a2.attnum = pos.col
  WHERE ci.relam = (SELECT oid FROM pg_am WHERE amname = 'btree')
    AND ci.relpages > 0
),

index_stats AS (
  SELECT
    ia.indexrelid,
    ia.indrelid,
    ia.index_name,
    ia.reltuples,
    ia.relpages,
    ia.relnamespace,
    ia.fillfactor,
    CASE WHEN MAX(COALESCE(s.null_frac, 0)) = 0
         THEN c.index_tuple_hdr
         ELSE c.index_tuple_hdr + ((32 + 8 - 1) / 8)
    END AS index_tuple_hdr_bm,
    SUM((1 - COALESCE(s.null_frac, 0)) * COALESCE(s.avg_width, 1024)) AS data_width,
    -- Unreliable when a key column is of type "name" (bad pg_stats widths) or
    -- has no statistics at all (LEFT JOIN miss). The latter used to be an INNER
    -- JOIN, which silently dropped such indexes from the report entirely.
    MAX(CASE WHEN ia.atttypid = 'pg_catalog.name'::regtype OR s.attname IS NULL THEN 1
             ELSE 0 END) > 0 AS is_na,
    c.block_size,
    c.maxalign,
    c.page_header,
    c.page_opaque,
    c.item_pointer
  FROM index_attrs ia
  JOIN pg_namespace n ON n.oid = ia.relnamespace
  LEFT JOIN pg_stats s
    ON s.schemaname = n.nspname
    AND s.tablename = ia.stats_relname
    AND s.attname = ia.attname
  CROSS JOIN constants c
  GROUP BY
    ia.indexrelid, ia.indrelid, ia.index_name,
    ia.reltuples, ia.relpages, ia.relnamespace, ia.fillfactor,
    c.block_size, c.maxalign, c.page_header, c.page_opaque,
    c.item_pointer, c.index_tuple_hdr
),

tuple_size_calculation AS (
  SELECT
    indexrelid,
    indrelid,
    index_name,
    reltuples,
    relpages,
    relnamespace,
    fillfactor,
    is_na,
    block_size,
    page_header,
    page_opaque,
    item_pointer,
    (
      index_tuple_hdr_bm
      + maxalign - CASE
          WHEN index_tuple_hdr_bm % maxalign = 0 THEN maxalign
          ELSE index_tuple_hdr_bm % maxalign
        END
      + data_width
      + maxalign - CASE
          WHEN data_width = 0 THEN 0
          WHEN data_width::int % maxalign = 0 THEN maxalign
          ELSE data_width::int % maxalign
        END
    )::numeric AS tuple_size
  FROM index_stats
),

bloat_estimation AS (
  SELECT
    indexrelid,
    indrelid,
    index_name,
    reltuples,
    relpages,
    relnamespace,
    fillfactor,
    is_na,
    block_size,
    COALESCE(1 +
      CEIL(reltuples / FLOOR(
        (block_size - page_opaque - page_header) * fillfactor
        / (100 * (item_pointer + tuple_size)::float)
      )), 0
    )::bigint AS min_pages_required
  FROM tuple_size_calculation
)

SELECT
  n.nspname || '.' || ct.relname AS table_name,
  n.nspname || '.' || be.index_name AS index_name,
  (be.relpages * be.block_size)::bigint AS index_size,
  be.fillfactor,
  be.relpages AS actual_pages,
  be.min_pages_required,
  GREATEST(0, be.relpages - be.min_pages_required) AS bloat_pages,
  ROUND(
    (100.0 * GREATEST(0, be.relpages - be.min_pages_required)
     / NULLIF(be.relpages, 0)
    )::numeric, 1
  ) AS bloat_pct,
  (GREATEST(0, be.relpages - be.min_pages_required) * be.block_size)::bigint AS bloat_size,
  be.is_na
FROM bloat_estimation be
JOIN pg_class ct ON ct.oid = be.indrelid
JOIN pg_namespace n ON n.oid = be.relnamespace
-- System schemas are kept here and filtered by the application (see --system).
ORDER BY bloat_pct DESC NULLS LAST, (be.relpages * be.block_size) DESC
`

// DetectBtreeIndexBloat analyzes B-Tree indexes for bloat using catalog statistics.
// No helper function needed — this is a pure SELECT on system catalogs.
func DetectBtreeIndexBloat(ctx context.Context, dbConn *db.DB) ([]BloatIndex, error) {
	slog.Info("Analyzing B-Tree index bloat...")

	rows, err := dbConn.Query(ctx, btreeBloatQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch index bloat data: %w", err)
	}
	defer rows.Close()

	var results []BloatIndex

	for rows.Next() {
		var (
			tableName  string
			indexName  string
			indexSize  int64
			fillfactor int
			pages      int
			minPages   int64
			bloatPages int64
			bloatPct   *float64
			bloatSize  int64
			isNA       bool
		)

		err := rows.Scan(
			&tableName,
			&indexName,
			&indexSize,
			&fillfactor,
			&pages,
			&minPages,
			&bloatPages,
			&bloatPct,
			&bloatSize,
			&isNA,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %w", err)
		}

		// Parse "schema.name" format
		tblParts := strings.Split(tableName, ".")
		var tblSchema, tblName string
		if len(tblParts) == 2 {
			tblSchema = tblParts[0]
			tblName = tblParts[1]
		} else {
			tblSchema = "public"
			tblName = tableName
		}

		idxParts := strings.Split(indexName, ".")
		var idxName string
		if len(idxParts) == 2 {
			idxName = idxParts[1]
		} else {
			idxName = indexName
		}

		idx := BloatIndex{
			Schema:     tblSchema,
			IndexName:  idxName,
			TableName:  tblName,
			IndexSize:  indexSize,
			Pages:      pages,
			MinPages:   minPages,
			BloatPages: bloatPages,
			BloatSize:  bloatSize,
			FillFactor: fillfactor,
			IsNA:       isNA,
			BloatPct:   bloatPct,
		}

		// BloatRatio for backward compatibility
		if bloatPct != nil {
			idx.BloatRatio = *bloatPct
		}

		results = append(results, idx)
	}

	slog.Info("B-Tree index analysis complete", "indexes", len(results))

	return results, nil
}
