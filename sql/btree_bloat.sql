-- WARNING: executed with a non-superuser role, the query inspect only index on tables you are granted to read.
-- WARNING: rows with is_na = 't' are known to have bad statistics ("name" type is not supported).
-- This query is compatible with PostgreSQL 8.2 and after
SELECT current_database(), nspname AS schemaname, tblname, idxname, bs*(relpages)::bigint AS real_size,
  bs*(relpages-est_pages)::bigint AS extra_size,
  100 * (relpages-est_pages)::float / relpages AS extra_pct,
  fillfactor,
  CASE WHEN relpages > est_pages_ff
    THEN bs*(relpages-est_pages_ff)
    ELSE 0
  END AS bloat_size,
  100 * (relpages-est_pages_ff)::float / relpages AS bloat_pct,
  is_na
  -- , 100-(pst).avg_leaf_density AS pst_avg_bloat, est_pages, index_tuple_hdr_bm, maxalign, pagehdr, nulldatawidth, nulldatahdrwidth, reltuples, relpages -- (DEBUG INFO)
FROM (
  SELECT coalesce(1 +
         ceil(reltuples/floor((bs-pageopqdata-pagehdr)/(4+nulldatahdrwidth)::float)), 0 -- ItemIdData size + computed avg size of a tuple (nulldatahdrwidth)
      ) AS est_pages,
      coalesce(1 +
         ceil(reltuples/floor((bs-pageopqdata-pagehdr)*fillfactor/(100*(4+nulldatahdrwidth)::float))), 0
      ) AS est_pages_ff,
      bs, nspname, tblname, idxname, relpages, fillfactor, is_na
      -- , pgstatindex(idxoid) AS pst, index_tuple_hdr_bm, maxalign, pagehdr, nulldatawidth, nulldatahdrwidth, reltuples -- (DEBUG INFO)
  FROM (
      SELECT maxalign, bs, nspname, tblname, idxname, reltuples, relpages, idxoid, fillfactor,
            ( index_tuple_hdr_bm +
                maxalign - CASE -- Add padding to the index tuple header to align on MAXALIGN
                  WHEN index_tuple_hdr_bm%maxalign = 0 THEN maxalign
                  ELSE index_tuple_hdr_bm%maxalign
                END
              + nulldatawidth + maxalign - CASE -- Add padding to the data to align on MAXALIGN
                  WHEN nulldatawidth = 0 THEN 0
                  WHEN nulldatawidth::integer%maxalign = 0 THEN maxalign
                  ELSE nulldatawidth::integer%maxalign
                END
            )::numeric AS nulldatahdrwidth, pagehdr, pageopqdata, is_na
            -- , index_tuple_hdr_bm, nulldatawidth -- (DEBUG INFO)
      FROM (
          SELECT n.nspname, i.tblname, i.idxname, i.reltuples, i.relpages,
              i.idxoid, i.fillfactor, current_setting('block_size')::numeric AS bs,
              CASE -- MAXALIGN: 4 on 32bits, 8 on 64bits (and mingw32 ?)
                WHEN version() ~ 'mingw32' OR version() ~ '64-bit|x86_64|ppc64|ia64|amd64' THEN 8
                ELSE 4
              END AS maxalign,
              /* per page header, fixed size: 20 for 7.X, 24 for others */
              24 AS pagehdr,
              /* per page btree opaque data */
              16 AS pageopqdata,
              /* per tuple header: add IndexAttributeBitMapData if some cols are null-able */
              CASE WHEN max(coalesce(s.null_frac,0)) = 0
                  THEN 8 -- IndexTupleData size
                  ELSE 8 + (( 32 + 8 - 1 ) / 8) -- IndexTupleData size + IndexAttributeBitMapData size ( max num filed per index + 8 - 1 /8)
              END AS index_tuple_hdr_bm,
              /* data len: we remove null values save space using it fractionnal part from stats */
              sum( (1-coalesce(s.null_frac, 0)) * coalesce(s.avg_width, 1024)) AS nulldatawidth,
              max( CASE WHEN i.atttypid = 'pg_catalog.name'::regtype THEN 1 ELSE 0 END ) > 0 AS is_na
          FROM (
              SELECT ci.relname AS tblname, ci.relnamespace, ic.idxname, ic.attpos, ic.indkey, ic.indkey[ic.attpos], ic.reltuples, ic.relpages, ic.tbloid, ic.idxoid, ic.fillfactor,
                  coalesce(a1.attnum, a2.attnum) AS attnum, coalesce(a1.attname, a2.attname) AS attname, coalesce(a1.atttypid, a2.atttypid) AS atttypid,
                  CASE WHEN a1.attnum IS NULL
                  THEN ic.idxname
                  ELSE ci.relname
                  END AS attrelname
              FROM (
                  SELECT 
                      ci.relname,                 -- Index name
                      ci.reltuples,               -- Estimated number of tuples in the index
                      ci.relpages,                -- Number of pages used by the index
                      i.indrelid AS tbloid,       -- OID of the table the index belongs to
                      i.indexrelid AS idxoid,     -- OID of the index itself
                      COALESCE(
                          substring(array_to_string(ci.reloptions, ' ') FROM 'fillfactor=([0-9]+)')::smallint, 90
                      ) AS fillfactor,            -- Fillfactor of the index (default 90 if not set)
                      gs.attpos,                  -- Attribute position (extra column: position from 1 to indnatts)
                      pg_catalog.string_to_array(
                          pg_catalog.textin(pg_catalog.int2vectorout(i.indkey)), ' '
                      )::int[] AS indkey          -- Array of indexed column positions in the table
                  FROM pg_catalog.pg_index i
                  JOIN pg_catalog.pg_class ci 
                      ON ci.oid = i.indexrelid
                  CROSS JOIN LATERAL generate_series(1, i.indnatts) AS gs(attpos)
                  WHERE ci.relam = (SELECT oid FROM pg_am WHERE amname = 'btree')  -- Only B-Tree indexes
                    AND ci.relpages > 0  -- Exclude empty indexes
                  ) AS idx_data
                ) AS ic
              JOIN pg_catalog.pg_class ct ON ci.oid = ic.tbloid
              LEFT JOIN pg_catalog.pg_attribute a1 ON
                  ic.indkey[ic.attpos] <> 0
                  AND a1.attrelid = ic.tbloid
                  AND a1.attnum = ic.indkey[ic.attpos]
              LEFT JOIN pg_catalog.pg_attribute a2 ON
                  ic.indkey[ic.attpos] = 0
                  AND a2.attrelid = ic.idxoid
                  AND a2.attnum = ic.attpos
            ) i
            JOIN pg_catalog.pg_namespace n ON n.oid = i.relnamespace
            JOIN pg_catalog.pg_stats s ON s.schemaname = n.nspname
                                      AND s.tablename = i.attrelname
                                      AND s.attname = i.attname
            GROUP BY 1,2,3,4,5,6,7,8,9,10,11
      ) AS rows_data_stats
  ) AS rows_hdr_pdg_stats
) AS relation_stats
ORDER BY nspname, tblname, idxname;



-- rows_data_stats
SELECT 
    n.nspname              ,               -- Schema name of the table owning the index
    i.tblname,                             -- Table name (owner of the index)
    i.idxname,                             -- Index name
    i.reltuples,                           -- Estimated number of tuples in the index
    i.relpages,                            -- Number of pages used by the index
    i.idxoid,                              -- OID of the index
    i.fillfactor,                          -- Index fillfactor (default 90 if not set)
    8192 AS bs,                            -- Block size in bytes
    8 AS maxalign,                         -- 8-bytes alignment margin on 64-bit systems
    24 AS pagehdr,                         -- Fixed page header size (24 bytes)
    16 AS pageopqdata,                     -- Opaque B-Tree data size per page (16 bytes)
    CASE  -- Estimate index tuple header size, adding extra space if some columns are nullable
      WHEN MAX(COALESCE(s.null_frac, 0)) = 0 THEN 8  
      ELSE 8 + ((32 + 8 - 1) / 8)  
    END AS index_tuple_hdr_bm,
    SUM((1 - COALESCE(s.null_frac, 0)) * COALESCE(s.avg_width, 1024)) AS nulldatawidth,  
                                             -- Total average data width (adjusted by null fraction)
    MAX(CASE WHEN i.atttypid = 'pg_catalog.name'::regtype THEN 1 ELSE 0 END) > 0 AS is_na  
                                             -- Flag indicating if any indexed column is of type "name"
FROM (
    -- Subquery to retrieve index attribute details
    SELECT 
        ct.relname AS tblname,            -- Table name (owner of the index)
        ct.relnamespace,                  -- Namespace OID of the table
        ic.idxname,                       -- Index name
        ic.attpos,                        -- Generated attribute position (1 to indnatts)
        ic.indkey,                        -- Array of indexed column positions (from the index)
        ic.indkey[ic.attpos] AS key_at_position,  
                                          -- Indexed column number at the current attribute position
        ic.reltuples,                     -- Estimated number of tuples in the index
        ic.relpages,                      -- Number of pages used by the index
        ic.tbloid,                        -- OID of the table the index belongs to
        ic.idxoid,                        -- OID of the index
        ic.fillfactor,                    -- Index fillfactor (default 90 if not set)
        COALESCE(a1.attnum, a2.attnum) AS attnum,  
                                          -- Attribute number from either table (a1) or index (a2)
        COALESCE(a1.attname, a2.attname) AS attname,  
                                          -- Attribute name from either table (a1) or index (a2)
        COALESCE(a1.atttypid, a2.atttypid) AS atttypid,  
                                          -- Data type OID of the attribute
        CASE 
          WHEN a1.attnum IS NULL THEN ic.idxname  -- If a1 is null, the attribute originates from the index
          ELSE ct.relname                         -- Otherwise, it originates from the table
        END AS attrelname                      -- The relation name for the attribute (table or index)
    FROM (
        -- Subquery to extract basic index metadata
        SELECT 
            ci.relname AS idxname,         -- Index name
            ci.reltuples,                  -- Estimated number of tuples in the index
            ci.relpages,                   -- Number of pages used by the index
            i.indrelid AS tbloid,          -- OID of the table the index belongs to
            i.indexrelid AS idxoid,        -- OID of the index itself
            COALESCE(
                substring(array_to_string(ci.reloptions, ' ') 
                          FROM 'fillfactor=([0-9]+)')::smallint, 90
            ) AS fillfactor,               -- Index fillfactor (default 90 if not set)
            gs.attpos,                     -- Generated attribute position (from 1 to indnatts)
            pg_catalog.string_to_array(
                pg_catalog.textin(pg_catalog.int2vectorout(i.indkey)), ' '
            )::int[] AS indkey             -- Array of indexed column positions in the table
        FROM pg_catalog.pg_index i
        JOIN pg_catalog.pg_class ci 
            ON ci.oid = i.indexrelid
        CROSS JOIN LATERAL generate_series(1, i.indnatts) AS gs(attpos)
        WHERE ci.relam = (SELECT oid FROM pg_am WHERE amname = 'btree') -- Only B-Tree indexes
          AND ci.relpages > 0           -- Exclude empty indexes
    ) AS ic
    JOIN pg_catalog.pg_class ct 
        ON ct.oid = ic.tbloid           -- Join to obtain table information for the index owner
    LEFT JOIN pg_catalog.pg_attribute a1 
        ON ic.indkey[ic.attpos] <> 0
       AND a1.attrelid = ic.tbloid
       AND a1.attnum = ic.indkey[ic.attpos]  
         -- Join to get attribute details from the table
    LEFT JOIN pg_catalog.pg_attribute a2 
        ON ic.indkey[ic.attpos] = 0
       AND a2.attrelid = ic.idxoid
       AND a2.attnum = ic.attpos  
         -- Join to get attribute details from the index (if indkey is 0)
) i
JOIN pg_catalog.pg_namespace n 
    ON n.oid = i.relnamespace       -- Join to get the schema name from the namespace OID
JOIN pg_catalog.pg_stats s 
    ON s.schemaname = n.nspname
   AND s.tablename = i.attrelname   -- Match table name (or index name) from attribute info
   AND s.attname = i.attname        -- Match attribute name from attribute info
GROUP BY 1,2,3,4,5,6,7,8,9,10,11;  -- Using numeric references for grouping


-- rows_hdr_pdg_stats
SELECT maxalign, bs, nspname, tblname, idxname, reltuples, relpages, idxoid, fillfactor,
            ( index_tuple_hdr_bm +
                maxalign - CASE -- Add padding to the index tuple header to align on MAXALIGN
                  WHEN index_tuple_hdr_bm%maxalign = 0 THEN maxalign
                  ELSE index_tuple_hdr_bm%maxalign
                END
              + nulldatawidth + maxalign - CASE -- Add padding to the data to align on MAXALIGN
                  WHEN nulldatawidth = 0 THEN 0
                  WHEN nulldatawidth::integer%maxalign = 0 THEN maxalign
                  ELSE nulldatawidth::integer%maxalign
                END
            )::numeric AS nulldatahdrwidth, pagehdr, pageopqdata, is_na
            -- , index_tuple_hdr_bm, nulldatawidth -- (DEBUG INFO)
      FROM (
          SELECT 
    n.nspname              ,               -- Schema name of the table owning the index
    i.tblname,                             -- Table name (owner of the index)
    i.idxname,                             -- Index name
    i.reltuples,                           -- Estimated number of tuples in the index
    i.relpages,                            -- Number of pages used by the index
    i.idxoid,                              -- OID of the index
    i.fillfactor,                          -- Index fillfactor (default 90 if not set)
    8192 AS bs,                            -- Block size in bytes
    8 AS maxalign,                         -- 8-bytes alignment margin on 64-bit systems
    24 AS pagehdr,                         -- Fixed page header size (24 bytes)
    16 AS pageopqdata,                     -- Opaque B-Tree data size per page (16 bytes)
    CASE  -- Estimate index tuple header size, adding extra space if some columns are nullable
      WHEN MAX(COALESCE(s.null_frac, 0)) = 0 THEN 8  
      ELSE 8 + ((32 + 8 - 1) / 8)  
    END AS index_tuple_hdr_bm,
    SUM((1 - COALESCE(s.null_frac, 0)) * COALESCE(s.avg_width, 1024)) AS nulldatawidth,  
                                             -- Total average data width (adjusted by null fraction)
    MAX(CASE WHEN i.atttypid = 'pg_catalog.name'::regtype THEN 1 ELSE 0 END) > 0 AS is_na  
                                             -- Flag indicating if any indexed column is of type "name"
FROM (
    -- Subquery to retrieve index attribute details
    SELECT 
        ct.relname AS tblname,            -- Table name (owner of the index)
        ct.relnamespace,                  -- Namespace OID of the table
        ic.idxname,                       -- Index name
        ic.attpos,                        -- Generated attribute position (1 to indnatts)
        ic.indkey,                        -- Array of indexed column positions (from the index)
        ic.indkey[ic.attpos] AS key_at_position,  
                                          -- Indexed column number at the current attribute position
        ic.reltuples,                     -- Estimated number of tuples in the index
        ic.relpages,                      -- Number of pages used by the index
        ic.tbloid,                        -- OID of the table the index belongs to
        ic.idxoid,                        -- OID of the index
        ic.fillfactor,                    -- Index fillfactor (default 90 if not set)
        COALESCE(a1.attnum, a2.attnum) AS attnum,  
                                          -- Attribute number from either table (a1) or index (a2)
        COALESCE(a1.attname, a2.attname) AS attname,  
                                          -- Attribute name from either table (a1) or index (a2)
        COALESCE(a1.atttypid, a2.atttypid) AS atttypid,  
                                          -- Data type OID of the attribute
        CASE 
          WHEN a1.attnum IS NULL THEN ic.idxname  -- If a1 is null, the attribute originates from the index
          ELSE ct.relname                         -- Otherwise, it originates from the table
        END AS attrelname                      -- The relation name for the attribute (table or index)
    FROM (
        -- Subquery to extract basic index metadata
        SELECT 
            ci.relname AS idxname,         -- Index name
            ci.reltuples,                  -- Estimated number of tuples in the index
            ci.relpages,                   -- Number of pages used by the index
            i.indrelid AS tbloid,          -- OID of the table the index belongs to
            i.indexrelid AS idxoid,        -- OID of the index itself
            COALESCE(
                substring(array_to_string(ci.reloptions, ' ') 
                          FROM 'fillfactor=([0-9]+)')::smallint, 90
            ) AS fillfactor,               -- Index fillfactor (default 90 if not set)
            gs.attpos,                     -- Generated attribute position (from 1 to indnatts)
            pg_catalog.string_to_array(
                pg_catalog.textin(pg_catalog.int2vectorout(i.indkey)), ' '
            )::int[] AS indkey             -- Array of indexed column positions in the table
        FROM pg_catalog.pg_index i
        JOIN pg_catalog.pg_class ci 
            ON ci.oid = i.indexrelid
        CROSS JOIN LATERAL generate_series(1, i.indnatts) AS gs(attpos)
        WHERE ci.relam = (SELECT oid FROM pg_am WHERE amname = 'btree') -- Only B-Tree indexes
          AND ci.relpages > 0           -- Exclude empty indexes
    ) AS ic
    JOIN pg_catalog.pg_class ct 
        ON ct.oid = ic.tbloid           -- Join to obtain table information for the index owner
    LEFT JOIN pg_catalog.pg_attribute a1 
        ON ic.indkey[ic.attpos] <> 0
       AND a1.attrelid = ic.tbloid
       AND a1.attnum = ic.indkey[ic.attpos]  
         -- Join to get attribute details from the table
    LEFT JOIN pg_catalog.pg_attribute a2 
        ON ic.indkey[ic.attpos] = 0
       AND a2.attrelid = ic.idxoid
       AND a2.attnum = ic.attpos  
         -- Join to get attribute details from the index (if indkey is 0)
) i
JOIN pg_catalog.pg_namespace n 
    ON n.oid = i.relnamespace       -- Join to get the schema name from the namespace OID
JOIN pg_catalog.pg_stats s 
    ON s.schemaname = n.nspname
   AND s.tablename = i.attrelname   -- Match table name (or index name) from attribute info
   AND s.attname = i.attname        -- Match attribute name from attribute info
GROUP BY 1,2,3,4,5,6,7,8,9,10,11  -- Using numeric references for grouping
      ) AS rows_data_stats
