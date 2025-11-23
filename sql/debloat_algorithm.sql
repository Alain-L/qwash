-- ============================================================================
-- qwash Debloat Algorithm - Core SQL Queries
-- ============================================================================
-- This file documents the SQL queries used by qwash's debloat algorithm.
-- The algorithm works by iteratively moving rows from high-numbered pages
-- to low-numbered pages, then reclaiming freed space with VACUUM.
--
-- Algorithm Overview:
-- 1. Identify the highest N pages in the table (by ctid)
-- 2. Copy rows from those pages to a temporary table
-- 3. Delete rows from the original table
-- 4. Reinsert rows (PostgreSQL places them in lower pages)
-- 5. VACUUM to reclaim freed space
-- 6. Repeat until bloat is eliminated
--
-- Parameters:
--   - :table_name : Name of the table to debloat
--   - :page_count : Number of pages to process per iteration (default: 5)
--   - :page_ids   : Array of page IDs identified in step 1
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Step 1: Identify highest pages in the table
-- ----------------------------------------------------------------------------
-- Extracts page numbers from CTIDs, groups by page, and selects the highest
-- N pages (those most likely to contain bloat at the end of the table).
--
-- Example output:
--   page
--  ------
--   4189
--   4188
--   4187
--   4186
--   4185
--
SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
FROM :table_name
GROUP BY page
ORDER BY page DESC
LIMIT :page_count;


-- ----------------------------------------------------------------------------
-- Step 2: Create temporary table with rows from highest pages
-- ----------------------------------------------------------------------------
-- Creates a temporary table containing all rows from the identified pages.
-- The temporary table is automatically dropped at transaction end (ON COMMIT DROP).
--
-- Note: The IN clause uses parameterized placeholders ($1, $2, ..., $N)
--       for the page IDs identified in step 1.
--
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM :table_name
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (:page_ids);


-- ----------------------------------------------------------------------------
-- Step 3: Delete rows from highest pages in original table
-- ----------------------------------------------------------------------------
-- Deletes the rows that were just copied to the temporary table.
-- This frees space in the high-numbered pages.
--
DELETE FROM :table_name
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (:page_ids);


-- ----------------------------------------------------------------------------
-- Step 4: Reinsert rows from temporary table
-- ----------------------------------------------------------------------------
-- Reinserts the saved rows back into the table.
-- PostgreSQL's insert logic places them in the lowest available pages,
-- effectively compacting the table.
--
INSERT INTO :table_name
SELECT * FROM qwash;


-- ----------------------------------------------------------------------------
-- Step 5: VACUUM to reclaim freed space
-- ----------------------------------------------------------------------------
-- After several iterations, VACUUM is run to truncate freed pages at the
-- end of the table file, actually reducing the table's on-disk size.
--
-- Frequency: Every ~250 pages processed (or ~50 iterations with page_count=5)
--
VACUUM :table_name;


-- ----------------------------------------------------------------------------
-- Step 6: ANALYZE to update statistics
-- ----------------------------------------------------------------------------
-- Updates table statistics after VACUUM to ensure the query planner has
-- accurate information about the table's new size and distribution.
--
ANALYZE :table_name;


-- ============================================================================
-- Supporting Queries
-- ============================================================================

-- Verify debloat progress: Show highest CTIDs after compaction
-- This helps confirm that rows have been successfully moved to lower pages.
--
SELECT ctid
FROM :table_name
ORDER BY ctid DESC
LIMIT 2;


-- List all databases (for multi-database operations)
--
SELECT datname
FROM pg_database
WHERE datistemplate = false
ORDER BY datname;


-- List all user tables (excluding system tables)
--
SELECT relname
FROM pg_class c
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname NOT IN ('pg_catalog', 'pg_toast', 'information_schema')
ORDER BY c.relname;
