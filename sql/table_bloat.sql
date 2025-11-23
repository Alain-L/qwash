-- ajouter blocs
SELECT 
  schemaname || '.' || tblname AS table_name,
  n_live_tup AS live_tup,
  (GREATEST(0, tblpages - est_tblpages) * (((bs - 24) * (fillfactor::numeric/100) ) / tpl_size ))::bigint AS dead_tup,
  est_tblpages AS min_pages_required,
  tblpages AS actual_pages,
  fillfactor,
  pg_size_pretty(
  pg_relation_size(format('%I.%I', schemaname, tblname)::regclass)
) AS relation_size,
COALESCE(
  CASE WHEN reltoastrelid <> 0 
       THEN pg_size_pretty(pg_relation_size(reltoastrelid::regclass)) 
       ELSE 'N/A' 
  END, 'N/A') AS "TOAST_size",
  pg_size_pretty((GREATEST(0, tblpages - est_tblpages) * bs)::bigint) AS bloat_size,
  ROUND((100 * GREATEST(0, tblpages - est_tblpages) / NULLIF(tblpages, 0))::numeric, 2) AS bloat_pct
FROM 
(
  SELECT 
    ceil(reltuples / (((bs - page_hdr) * fillfactor) / (tpl_size * 100))) AS est_tblpages, -- Estimated pages required
    tblpages, 
    bs, 
    schemaname, 
    tblname,
    tpl_size,
    fillfactor,
    n_live_tup,
    reltoastrelid
  FROM (
    SELECT
      (4 -- object identifier pointer
       + tpl_hdr_size + tpl_data_size + (2 * ma)
       - CASE WHEN tpl_hdr_size % ma = 0 THEN ma ELSE tpl_hdr_size % ma END
       - CASE WHEN ceil(tpl_data_size)::int % ma = 0 THEN ma ELSE ceil(tpl_data_size)::int % ma END
      ) AS tpl_size, -- Estimated tuple size
      bs - page_hdr AS size_per_block, -- Usable space per block
      heappages AS tblpages, -- Total pages used (heap only, TOAST removed)
      reltuples, -- Estimated number of tuples
      bs, -- Block size
      page_hdr, -- Page header size (24 bytes)
      schemaname, 
      tblname, 
      fillfactor,
      n_live_tup,
      reltoastrelid
    FROM (
      SELECT
        ns.nspname AS schemaname, -- Schema name
        psut.n_live_tup AS n_live_tup, -- number of alive tuples
        psut.n_dead_tup AS n_dead_tup, -- number of non VACUUMed tuples 
        tbl.relname AS tblname, -- Table name
        tbl.oid AS tbl_oid, -- Table OID for size calculations
        tbl.reltuples, -- Estimated number of rows
        tbl.relpages AS heappages, -- Heap pages count
        tbl.reltoastrelid AS reltoastrelid, -- TOAST relation OID
        8192 AS bs, -- PostgreSQL block size setting
        8 AS ma, -- Assuming 64-bit system with 8-byte alignment
        24 AS page_hdr, -- Fixed page header size
        23 + ((7 + COUNT(s.attname)) / 8) AS tpl_hdr_size, -- Fixed tuple header size + NULL bitmap
        SUM((1 - COALESCE(s.null_frac, 0)) * COALESCE(s.avg_width, 0)) AS tpl_data_size, -- Estimated tuple data size
        COALESCE(
          substring(array_to_string(tbl.reloptions, ' ') FROM 'fillfactor=([0-9]+)')::smallint,
          100
        ) AS fillfactor -- Extracted fillfactor (default 100)
      FROM pg_attribute att
      JOIN pg_class tbl ON att.attrelid = tbl.oid
      JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
      JOIN pg_stat_user_tables psut ON psut.relid = tbl.oid
      LEFT JOIN pg_stats s ON s.schemaname = ns.nspname
           AND s.tablename = tbl.relname 
           AND s.inherited = false 
           AND s.attname = att.attname
      WHERE NOT att.attisdropped
        AND tbl.relkind IN ('r','m') -- Regular tables and materialized views only
        AND ns.nspname NOT IN ('pg_catalog', 'information_schema') -- Exclude system schemas
      GROUP BY 
      ns.nspname, tbl.relname, tbl.oid, tbl.reltuples, tbl.relpages, tbl.reloptions, psut.n_live_tup, psut.n_dead_tup
    ) s -- get infos from pg_stats
  ) s2 -- tuple size estimation
) s3 -- bloat computation
ORDER BY bloat_pct DESC; -- Sort by bloat percentage