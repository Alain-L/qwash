-- B-Tree Index Bloat Estimation Query
-- Estimates B-Tree index bloat without requiring pgstattuple extension
--
-- Strategy: Compare theoretical minimum pages (based on tuple count, key width
--           from pg_stats, fillfactor, and B-Tree page overhead) with actual pages
--
-- Based on: ioguix btree bloat estimation query
-- Precision: good for indexes with up-to-date statistics (ANALYZE)
-- Warning: columns of type "name" produce unreliable pg_stats (is_na = true)
--
-- Compatible with PostgreSQL 9.6+

WITH constants AS (
  -- PostgreSQL internal constants for B-Tree bloat calculation
  SELECT
    current_setting('block_size')::int AS block_size,
    -- MAXALIGN: 8 bytes on 64-bit systems, 4 on 32-bit
    CASE WHEN version() ~ '64-bit|x86_64|ppc64|ia64|amd64' THEN 8
         ELSE 4
    END AS maxalign,
    24 AS page_header,    -- PageHeaderData (fixed per-page overhead)
    16 AS page_opaque,    -- BTPageOpaqueData (prev, next, level, flags)
    4 AS item_pointer,    -- ItemIdData (line pointer per tuple)
    8 AS index_tuple_hdr  -- IndexTupleData (base tuple header)
),

index_attrs AS (
  -- Resolve indexed columns, handling both table columns and expression indexes.
  -- Expression indexes have indkey[pos] = 0: their attributes come from the
  -- index relation itself, not the parent table.
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
      90                  -- B-Tree default fillfactor
    ) AS fillfactor,
    COALESCE(a1.attnum, a2.attnum) AS attnum,
    COALESCE(a1.attname, a2.attname) AS attname,
    COALESCE(a1.atttypid, a2.atttypid) AS atttypid,
    -- For pg_stats lookup: use table name for regular columns, index name for expressions
    CASE WHEN a1.attnum IS NULL THEN ci.relname
         ELSE ct.relname
    END AS stats_relname
  FROM pg_index ic
  JOIN pg_class ci ON ci.oid = ic.indexrelid
  JOIN pg_class ct ON ct.oid = ic.indrelid
  -- Iterate over each indexed column position
  CROSS JOIN LATERAL generate_series(1, ic.indnatts) AS pos(col)
  -- indkey as int array for element access (compatible with PG 9.6+)
  CROSS JOIN LATERAL (
    SELECT pg_catalog.string_to_array(
      pg_catalog.textin(pg_catalog.int2vectorout(ic.indkey)), ' '
    )::int[] AS arr
  ) AS indkey_arr
  -- Regular table column (indkey[pos] != 0)
  LEFT JOIN pg_attribute a1
    ON indkey_arr.arr[pos.col] <> 0
    AND a1.attrelid = ic.indrelid
    AND a1.attnum = indkey_arr.arr[pos.col]
  -- Expression index column (indkey[pos] = 0)
  LEFT JOIN pg_attribute a2
    ON indkey_arr.arr[pos.col] = 0
    AND a2.attrelid = ic.indexrelid
    AND a2.attnum = pos.col
  WHERE ci.relam = (SELECT oid FROM pg_am WHERE amname = 'btree')
    AND ci.relpages > 0               -- Exclude empty indexes
),

index_stats AS (
  -- Aggregate column statistics per index
  SELECT
    ia.indexrelid,
    ia.indrelid,
    ia.index_name,
    ia.reltuples,
    ia.relpages,
    ia.relnamespace,
    ia.fillfactor,
    -- Tuple header size: base 8 bytes + optional NULL bitmap (4 bytes)
    -- IndexAttributeBitMapData added when any indexed column is nullable
    CASE WHEN MAX(COALESCE(s.null_frac, 0)) = 0
         THEN c.index_tuple_hdr
         ELSE c.index_tuple_hdr + ((32 + 8 - 1) / 8) -- +4 bytes bitmap
    END AS index_tuple_hdr_bm,
    -- Total average data width, adjusted by null fraction per column
    SUM((1 - COALESCE(s.null_frac, 0)) * COALESCE(s.avg_width, 1024)) AS data_width,
    -- Flag: pg_stats is unreliable for "name" type columns
    MAX(CASE WHEN ia.atttypid = 'pg_catalog.name'::regtype THEN 1
             ELSE 0 END) > 0 AS is_na,
    c.block_size,
    c.maxalign,
    c.page_header,
    c.page_opaque,
    c.item_pointer
  FROM index_attrs ia
  JOIN pg_namespace n ON n.oid = ia.relnamespace
  JOIN pg_stats s
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
  -- Calculate the aligned average tuple size with MAXALIGN padding.
  -- Each index tuple = header (aligned) + data (aligned).
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
    -- Aligned tuple size (same formula as ioguix nulldatahdrwidth):
    -- = align(index_tuple_hdr_bm) + align(data_width)
    (
      index_tuple_hdr_bm
      + maxalign - CASE  -- Align header to MAXALIGN
          WHEN index_tuple_hdr_bm % maxalign = 0 THEN maxalign
          ELSE index_tuple_hdr_bm % maxalign
        END
      + data_width
      + maxalign - CASE  -- Align data to MAXALIGN
          WHEN data_width = 0 THEN 0
          WHEN data_width::int % maxalign = 0 THEN maxalign
          ELSE data_width::int % maxalign
        END
    )::numeric AS tuple_size
  FROM index_stats
),

bloat_estimation AS (
  -- Estimate minimum required pages vs actual pages used.
  -- min_pages_required includes +1 for the B-Tree metapage.
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
    -- Minimum pages with fillfactor applied
    -- Usable space per page = block_size - page_header - page_opaque
    -- Each tuple slot = item_pointer + tuple_size
    COALESCE(1 +
      CEIL(reltuples / FLOOR(
        (block_size - page_opaque - page_header) * fillfactor
        / (100 * (item_pointer + tuple_size)::float)
      )), 0
    )::bigint AS min_pages_required
  FROM tuple_size_calculation
)

-- Final output with human-readable formatting
SELECT
  n.nspname || '.' || ct.relname AS table_name,
  n.nspname || '.' || be.index_name AS index_name,
  pg_size_pretty((be.relpages * be.block_size)::bigint) AS index_size,
  be.fillfactor,
  be.relpages AS actual_pages,
  be.min_pages_required,
  GREATEST(0, be.relpages - be.min_pages_required) AS bloat_pages,
  ROUND(
    (100.0 * GREATEST(0, be.relpages - be.min_pages_required)
     / NULLIF(be.relpages, 0)
    )::numeric, 1
  ) AS bloat_pct,
  pg_size_pretty(
    (GREATEST(0, be.relpages - be.min_pages_required) * be.block_size)::bigint
  ) AS bloat_size,
  be.is_na
FROM bloat_estimation be
JOIN pg_class ct ON ct.oid = be.indrelid
JOIN pg_namespace n ON n.oid = be.relnamespace
WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY bloat_pct DESC NULLS LAST, (be.relpages * be.block_size) DESC;
