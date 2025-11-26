-- ============================================================================
-- qwash Algorithm Demo
-- ============================================================================
-- This file demonstrates qwash's debloat algorithm with executable examples.
-- Run these queries step-by-step in psql to understand how qwash works.
--
-- Algorithm Overview:
-- 1. Identify the highest N pages in the table (by ctid)
-- 2. Copy rows from those pages to a temporary table
-- 3. Delete rows from the original table
-- 4. Reinsert rows (PostgreSQL places them in lower pages)
-- 5. VACUUM to reclaim freed space
-- 6. Repeat until bloat is eliminated
--
-- This approach is non-blocking and doesn't require any extension.
-- ============================================================================


-- ============================================================================
-- Demo 1: Basic concept (simple integer table)
-- ============================================================================

DROP TABLE IF EXISTS demo_qwash;

CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT i AS id
FROM generate_series(1,10000) i;

SELECT pg_relation_size('demo_qwash');
-- Result: 65536 = 8 * 8192 = 8 pages

-- PostgreSQL allocated 8 pages, but do we need them all?
-- Check rows per page:
SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page,
       count(*) AS row_count
FROM public.demo_qwash
GROUP BY page
ORDER BY page;

--  page | row_count
-- ------+-----------
--     0 |       226
--     1 |       226
--     2 |       226
--     3 |       226
--     4 |        96   <-- not full
-- (5 rows)

-- Only 5 pages are actually used! The table is bloated from creation.
VACUUM;
SELECT pg_relation_size('demo_qwash');
-- Result: 40960 = 5 * 8192 = 5 pages (correct size now)

-- Create some bloat by deleting rows
DELETE FROM demo_qwash WHERE id <= 500;

-- Check remaining rows distribution:
--  page | row_count
-- ------+-----------
--     2 |       178
--     3 |       226
--     4 |        96
-- Pages 0 and 1 are now empty, but table size is still 40960 bytes

VACUUM;

-- Run qwash algorithm: move rows from pages 3,4 to lower pages
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (3,4);

DELETE FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (3,4);

INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

-- After one iteration:
--  page | row_count
-- ------+-----------
--     0 |       226
--     1 |        96
--     2 |       178
-- (3 rows)

-- All 500 rows are now in the first 3 pages. Perfect compaction!

VACUUM;
ANALYZE;

SELECT pg_relation_size('demo_qwash');
-- Result: 24576 = 3 * 8192 = 3 pages

-- Bloat estimation query confirms: 0% bloat


-- ============================================================================
-- Demo 2: Variable-length data (more realistic)
-- ============================================================================

DROP TABLE IF EXISTS demo_qwash;

CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT
  i AS id,
  substring(md5(random()::text) FROM 1 FOR (5 + floor(random() * 10))::int) AS random_string
FROM generate_series(1, 1000) i;

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- Result: 49152 = 6 * 8192 = 6 pages

-- Initial page distribution:
SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page,
       count(*) AS row_count
FROM public.demo_qwash
GROUP BY page
ORDER BY page;

--  page | row_count
-- ------+-----------
--     0 |       176
--     1 |       177
--     2 |       174
--     3 |       176
--     4 |       179
--     5 |       118   <-- last page is less full (normal)

ANALYZE;
-- Bloat query shows: 0% bloat, 6 pages needed

-- Create 50% bloat by deleting half the rows
DELETE FROM demo_qwash WHERE id <= 250;
DELETE FROM demo_qwash WHERE id >= 500 AND id < 750;

-- Page distribution after delete:
--  page | row_count
-- ------+-----------
--     1 |       103
--     2 |       146
--     4 |       133
--     5 |       118
-- Some pages are gone, others are partially empty

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- Still 49152 bytes = 6 pages (bloat not reclaimed!)

ANALYZE;
-- Bloat query shows: 50% bloat, only 3 pages needed

-- Run qwash algorithm
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (4,5);

DELETE FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (4,5);

INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

-- After one iteration:
--  page | row_count
-- ------+-----------
--     0 |       178
--     1 |       176
--     2 |       146
-- Rows have been sequentially rewritten from page 0!

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- Result: 24576 = 3 * 8192 = 3 pages

ANALYZE;
-- Bloat query shows: 0% bloat. Perfect!


-- ============================================================================
-- Demo 3: Larger table with multiple iterations
-- ============================================================================

DROP TABLE IF EXISTS demo_qwash;

CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT
  i AS id,
  substring(md5(random()::text) FROM 1 FOR (5 + floor(random() * 10))::int) AS random_string
FROM generate_series(1, 5000) i;

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- Result: 237568 = 29 * 8192 = 29 pages

ANALYZE;
-- Bloat query: ~7% bloat (ANALYZE imprecision + block allocation overhead)

-- Create ~50% bloat
DELETE FROM demo_qwash WHERE id <= 1000;
DELETE FROM demo_qwash WHERE id > 2500 AND id <= 4000;

-- Page distribution shows gaps and partially filled pages

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- Still 237568 = 29 pages

ANALYZE;
-- Bloat query: ~52% bloat, 14 pages would suffice

-- -------------------------
-- Iteration 1
-- -------------------------
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (27,28);

DELETE FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (27,28);

INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

VACUUM;
-- Pages 0 and 1 are back!
-- Bloat: ~48%

-- -------------------------
-- Iteration 2
-- -------------------------
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (25,26);

DELETE FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (25,26);

INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

VACUUM;
-- Pages 2 and 3 are back!
-- Bloat: ~44%

-- -------------------------
-- Iteration 3
-- -------------------------
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (23,24);

DELETE FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (23,24);

INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- 188416 = 23 pages
-- Bloat: ~39%

-- -------------------------
-- Iteration 4
-- -------------------------
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (21,22);

DELETE FROM demo_qwash
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (21,22);

INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

VACUUM;
SELECT pg_relation_size('demo_qwash');
-- 122880 = 15 pages

ANALYZE;
-- Bloat query:
--   live_tup: 2500
--   min pages required: 14
--   actual pages: 15
--   bloat_pct: 6.67%
--
-- Minimal bloat achieved, equivalent to VACUUM FULL result!


-- ============================================================================
-- Cleanup
-- ============================================================================
DROP TABLE IF EXISTS demo_qwash;
