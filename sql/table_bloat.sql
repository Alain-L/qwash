-- Table Bloat Estimation Query
-- Estimates table bloat without requiring pgstattuple extension
-- Uses PostgreSQL system catalogs and statistics to calculate wasted space
--
-- Strategy: Compare theoretical minimum pages (based on tuple count and size)
--           with actual pages used, accounting for fillfactor and alignment
--
-- Compatible with PostgreSQL 9.4+ (requires multiple CTEs)
--
-- Sizes (relation_size, TOAST_size, bloat_size) are returned in raw bytes so
-- the Go code consumes exact integers. To read this query by hand, wrap those
-- columns in pg_size_pretty(...).

WITH constants AS (
  -- PostgreSQL internal constants for bloat calculation
  -- These values are standard across all PostgreSQL installations
  SELECT
    8192 AS block_size,           -- Standard page size (8KB blocks)
    8 AS memory_alignment,        -- 64-bit system alignment (MAXALIGN)
    24 AS page_header_size,       -- Fixed overhead per page
    23 AS tuple_header_base       -- Base tuple header (before NULL bitmap)
),

table_stats AS (
  -- Gather table metadata and statistics from system catalogs
  SELECT
    ns.nspname AS schemaname,
    tbl.relname AS tblname,
    tbl.oid AS table_oid,
    tbl.reltuples,                -- Estimated row count (updated by ANALYZE)
    tbl.relpages AS heap_pages,   -- Total pages in table heap
    tbl.reltoastrelid,            -- OID of TOAST table (0 if none)
    psut.n_live_tup,              -- Live tuples from pg_stat_user_tables
    psut.n_dead_tup,              -- Dead tuples awaiting VACUUM
    COALESCE(
      substring(array_to_string(tbl.reloptions, ' ') FROM 'fillfactor=([0-9]+)')::smallint,
      100
    ) AS fillfactor,              -- Table fillfactor (default 100 = pack pages fully)
    -- Tuple header size: base + NULL bitmap
    -- NULL bitmap = (7 + column_count) / 8 bytes
    c.tuple_header_base + ((7 + COUNT(s.attname)) / 8) AS tuple_header_size,
    -- Total data size across all columns (weighted by NULL probability)
    SUM((1 - COALESCE(s.null_frac, 0)) * COALESCE(s.avg_width, 0)) AS tuple_data_size,
    c.block_size,
    c.memory_alignment,
    c.page_header_size
  FROM pg_attribute att
  JOIN pg_class tbl ON att.attrelid = tbl.oid
  JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
  -- pg_stat_all_tables (not _user_) so system catalogs are covered too; the
  -- application decides whether to show them (see --system). Stats for a user
  -- table are identical in both views.
  JOIN pg_stat_all_tables psut ON psut.relid = tbl.oid
  CROSS JOIN constants c
  LEFT JOIN pg_stats s
    ON s.schemaname = ns.nspname
    AND s.tablename = tbl.relname
    AND s.inherited = false
    AND s.attname = att.attname
  WHERE NOT att.attisdropped
    AND tbl.relkind IN ('r', 'm')  -- Regular tables and materialized views
  GROUP BY
    ns.nspname, tbl.relname, tbl.oid, tbl.reltuples, tbl.relpages,
    tbl.reloptions, tbl.reltoastrelid, psut.n_live_tup, psut.n_dead_tup,
    c.tuple_header_base, c.block_size, c.memory_alignment, c.page_header_size
),

tuple_size_calculation AS (
  -- Calculate actual tuple size with memory alignment padding
  -- PostgreSQL aligns data structures to MAXALIGN boundaries (8 bytes on 64-bit)
  SELECT
    schemaname,
    tblname,
    table_oid,
    reltuples,
    heap_pages,
    reltoastrelid,
    n_live_tup,
    n_dead_tup,
    fillfactor,
    block_size,
    page_header_size,
    -- Total tuple size calculation:
    -- = ItemPointerData (4 bytes)
    -- + tuple header
    -- + tuple data
    -- + alignment padding (pessimistic estimate)
    -- - over-counted alignment (correction)
    (
      4                              -- ItemPointerData (ctid pointer)
      + tuple_header_size
      + tuple_data_size
      + (2 * memory_alignment)       -- Pessimistic alignment overhead
      - CASE WHEN tuple_header_size % memory_alignment = 0
             THEN memory_alignment
             ELSE tuple_header_size % memory_alignment
        END                          -- Remove over-counted header padding
      - CASE WHEN CEIL(tuple_data_size)::int % memory_alignment = 0
             THEN memory_alignment
             ELSE CEIL(tuple_data_size)::int % memory_alignment
        END                          -- Remove over-counted data padding
    ) AS tuple_size,
    block_size - page_header_size AS usable_space_per_page
  FROM table_stats
),

bloat_estimation AS (
  -- Estimate minimum required pages vs actual pages used
  -- Bloat = actual_pages - estimated_min_pages
  SELECT
    schemaname,
    tblname,
    table_oid,
    reltuples,
    heap_pages AS actual_pages,
    reltoastrelid,
    n_live_tup,
    n_dead_tup,
    fillfactor,
    tuple_size,
    block_size,
    -- Minimum pages needed (accounting for fillfactor)
    -- Formula: ceil(tuples / (usable_space * fillfactor% / tuple_size))
    CEIL(
      reltuples / (
        (usable_space_per_page * fillfactor) / (tuple_size * 100.0)
      )
    )::bigint AS estimated_min_pages
  FROM tuple_size_calculation
)

-- Final output with human-readable formatting
SELECT
  schemaname || '.' || tblname AS table_name,
  n_live_tup AS live_tup,
  -- Estimated dead tuples from bloat pages
  -- Uses GREATEST(0, ...) to prevent negative bloat values
  (GREATEST(0, actual_pages - estimated_min_pages)
   * ((block_size - 24) * (fillfactor::numeric / 100.0))
   / tuple_size
  )::bigint AS dead_tup,
  estimated_min_pages AS min_pages_required,
  actual_pages,
  fillfactor,
  pg_relation_size(format('%I.%I', schemaname, tblname)::regclass)::bigint
    AS relation_size,
  CASE WHEN reltoastrelid <> 0
       THEN pg_relation_size(reltoastrelid::regclass)
       ELSE 0
  END::bigint AS "TOAST_size",
  -- Bloat size: use GREATEST to avoid negative values (over-estimated min_pages)
  (GREATEST(0, actual_pages - estimated_min_pages) * block_size)::bigint
    AS bloat_size,
  -- Bloat percentage: use GREATEST and NULLIF to handle edge cases
  ROUND(
    (100.0 * GREATEST(0, actual_pages - estimated_min_pages)
     / NULLIF(actual_pages, 0)
    )::numeric, 2
  ) AS bloat_pct,
  -- Stale/missing statistics: the estimate is driven by reltuples/relpages,
  -- which only VACUUM/ANALYZE refresh. It is unusable when:
  --   * the row count is unknown (reltuples < 0, never analyzed), or
  --   * the catalog reports 0 pages while the table holds data on disk, or
  --   * many dead tuples have accumulated since the last VACUUM (>5% of the
  --     table) — a DELETE/UPDATE-heavy table whose reltuples no longer matches
  --     reality, so the bloat would be badly under- or over-estimated.
  -- n_dead_tup is the same signal autovacuum uses to decide a VACUUM is due.
  (reltuples < 0
   OR (actual_pages = 0
       AND pg_relation_size(format('%I.%I', schemaname, tblname)::regclass) > 0)
   OR (COALESCE(n_dead_tup, 0) > 0
       AND COALESCE(n_dead_tup, 0)::float8
           / NULLIF(COALESCE(n_live_tup, 0) + COALESCE(n_dead_tup, 0), 0) > 0.05)
  ) AS stale_stats
FROM bloat_estimation
ORDER BY bloat_pct DESC;
