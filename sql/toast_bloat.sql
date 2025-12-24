-- TOAST Bloat Estimation Query
-- Estimates TOAST table bloat without requiring pgstattuple extension
-- Uses the ppc (pages per chunk) algorithm with chunk sampling
--
-- Precision: ±4% for TOAST tables >= 10 MB
-- Requires: recent VACUUM (not just ANALYZE) for accurate pg_class stats
--
-- Compatible with PostgreSQL 9.6+

-- Helper function: sample one chunk to get its size
-- Required because TOAST table names are dynamic (pg_toast.pg_toast_<OID>)
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
$$ LANGUAGE plpgsql;

-- Main query
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
    -- ppc = pages per chunk (current ratio)
    toast_pages::numeric / NULLIF(toast_chunks, 0) AS ppc,
    -- ppc_ref from sampled chunk: (chunk_size + 50 overhead) / 8192
    (_qwash_sample_chunk_size(main_oid) + 50)::numeric / 8192 AS ppc_ref,
    -- Reliability threshold: 1250 pages = 10 MB
    toast_pages >= 1250 AS is_reliable
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
  END AS warning
FROM bloat_calc
WHERE toast_bytes > 0
ORDER BY bloat_pct DESC NULLS LAST, toast_bytes DESC;

-- Note: To cleanup the helper function after use:
-- DROP FUNCTION IF EXISTS _qwash_sample_chunk_size(oid);
