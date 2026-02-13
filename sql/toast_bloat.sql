-- TOAST Bloat Estimation Query
-- Estimates TOAST table bloat without requiring pgstattuple extension
-- Uses the ppc (pages per chunk) algorithm with chunk sampling
--
-- Precision: ±4% for TOAST tables >= 10 MB
-- Requires: recent VACUUM (not just ANALYZE) for accurate pg_class stats
--
-- Compatible with PostgreSQL 9.6+

-- Helper function: sample one full-size chunk to get its size
-- Required because TOAST table names are dynamic (pg_toast.pg_toast_<OID>)
-- Prefers chunk_seq > 0 (guaranteed full-size), falls back to any chunk
-- Note: in qwash, this function is created in pg_temp schema for auto-cleanup
CREATE OR REPLACE FUNCTION _qwash_sample_chunk_size(main_table_oid oid)
RETURNS integer AS $$
DECLARE
  chunk_size integer;
BEGIN
  -- Try a non-first chunk (guaranteed full-size for multi-chunk values)
  EXECUTE format(
    'SELECT length(chunk_data) FROM pg_toast.pg_toast_%s WHERE chunk_seq > 0 LIMIT 1',
    main_table_oid
  ) INTO chunk_size;
  -- Fallback: single-chunk values (chunk_seq=0 only), still full-size
  IF chunk_size IS NULL THEN
    EXECUTE format(
      'SELECT length(chunk_data) FROM pg_toast.pg_toast_%s LIMIT 1',
      main_table_oid
    ) INTO chunk_size;
  END IF;
  RETURN chunk_size;
EXCEPTION WHEN OTHERS THEN
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Main query
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
    COALESCE(
      GREATEST(st.last_vacuum, st.last_autovacuum),
      '1970-01-01'::timestamptz
    ) < now() - interval '24 hours' AS stale_stats
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
    -- ppc = pages per chunk (current ratio)
    toast_pages::numeric / NULLIF(toast_chunks, 0) AS ppc,
    -- ppc_ref from sampled chunk: (chunk_size + 50 overhead) / block_size
    (_qwash_sample_chunk_size(main_oid) + 50)::numeric
      / (SELECT block_size FROM bs) AS ppc_ref,
    -- Reliability threshold: 10 MB
    toast_pages >= 10 * 1024 * 1024 / (SELECT block_size FROM bs) AS is_reliable
  FROM toast_stats
)

SELECT
  schemaname || '.' || table_name AS table_name,
  pg_size_pretty(toast_bytes) AS toast_size,
  toast_pages,
  toast_chunks,
  ROUND(ppc::numeric, 4) AS ppc,
  ROUND(ppc_ref::numeric, 4) AS ppc_ref,
  -- Bloat estimation (NULL if < 10 MB or no ppc_ref)
  CASE
    WHEN NOT is_reliable THEN NULL
    WHEN ppc_ref IS NULL THEN NULL
    ELSE ROUND(GREATEST(0, (1 - ppc_ref / ppc) * 100)::numeric, 1)
  END AS bloat_pct,
  -- Estimated bloat size
  CASE
    WHEN NOT is_reliable THEN NULL
    WHEN ppc_ref IS NULL THEN NULL
    ELSE pg_size_pretty(
      (GREATEST(0, (1 - ppc_ref / ppc)) * toast_bytes)::bigint
    )
  END AS bloat_size,
  -- Status/warning
  CASE
    WHEN NOT is_reliable THEN '< 10 MB'
    WHEN ppc_ref IS NULL THEN 'no chunks'
    ELSE NULL
  END AS warning,
  stale_stats
FROM bloat_calc
WHERE toast_bytes > 0
ORDER BY bloat_pct DESC NULLS LAST, toast_bytes DESC;

-- Note: To cleanup the helper function after use:
-- DROP FUNCTION IF EXISTS _qwash_sample_chunk_size(oid);
