-- qwash demo database setup
-- Creates tables with various bloat levels for testing all qwash features
--
-- Usage:
--   createdb -p 5438 qwash_demo
--   psql -p 5438 -d qwash_demo -f testdata/demo.sql
--
-- Tables created:
--   public.orders       — CRITICAL heap bloat (~70%)
--   public.sessions     — HIGH heap bloat (~35%)
--   public.products     — MEDIUM heap bloat (~15%)
--   public.customers    — NO SIGNIFICANT bloat (clean table)
--   public.audit_log    — CRITICAL TOAST bloat (~60%, >= 10 MB, 3 chunks/val, avg=1333)
--   public.toast_large   — TOAST 20 KB values (11 chunks/val, avg=1818, small Go vs standalone delta)
--   public.toast_narrow  — TOAST 2.1 KB values (2 chunks/val, avg=1050, huge Go vs standalone delta)
--   public.toast_aligned — TOAST 3992 B values (2 chunks/val, avg=1996, zero Go vs standalone delta)
--   public.toast_mixed   — TOAST mixed sizes (2.1/8/15 KB, tests sampling robustness)
--   public.toast_multi_col — TOAST 2 columns (3 KB + 2.5 KB, multi-column estimation test)
--   public.toast_compressed — TOAST EXTENDED storage (8 KB semi-compressible, >= 10 MB, tests compression handling)
--   public.notifications — UNRELIABLE TOAST (< 10 MB, STORAGE EXTERNAL)
--   archive.events      — HIGH heap bloat (~45%, separate schema for -n testing)

\timing on
\set ON_ERROR_STOP on

BEGIN;

-- ============================================================================
-- Schema setup
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS archive;

-- ============================================================================
-- Table definitions (autovacuum disabled to preserve bloat)
-- ============================================================================

-- orders: main demo table, critical bloat, multiple indexes
CREATE TABLE orders (
    id          serial PRIMARY KEY,
    customer_id integer NOT NULL,
    status      varchar(20) NOT NULL,
    amount      numeric(10,2) NOT NULL,
    created_at  timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

CREATE INDEX idx_orders_customer ON orders(customer_id);
CREATE INDEX idx_orders_created  ON orders(created_at);

-- sessions: medium bloat, index on token
CREATE TABLE sessions (
    id         serial PRIMARY KEY,
    user_id    integer NOT NULL,
    token      varchar(64) NOT NULL,
    ip_address inet,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

CREATE INDEX idx_sessions_user  ON sessions(user_id);
CREATE INDEX idx_sessions_token ON sessions(token);

-- products: low bloat
CREATE TABLE products (
    id          serial PRIMARY KEY,
    name        varchar(100) NOT NULL,
    description text,
    price       numeric(10,2) NOT NULL,
    category    varchar(50) NOT NULL,
    updated_at  timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

CREATE INDEX idx_products_category ON products(category);

-- customers: clean table, no bloat (contrast)
CREATE TABLE customers (
    id         serial PRIMARY KEY,
    email      varchar(255) NOT NULL UNIQUE,
    name       varchar(100) NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

-- audit_log: large TOAST columns, critical TOAST bloat
CREATE TABLE audit_log (
    id         serial PRIMARY KEY,
    event_type varchar(50) NOT NULL,
    payload    text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE audit_log ALTER COLUMN payload SET STORAGE EXTERNAL;

-- toast_large: 20 KB values → 11 chunks/value, tiny last chunk (40 B)
-- Tests: small discrepancy between Go sampling (avg=1818) and standalone (constant=1996)
CREATE TABLE toast_large (
    id         serial PRIMARY KEY,
    payload    text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE toast_large ALTER COLUMN payload SET STORAGE EXTERNAL;

-- toast_narrow: 2100 B values → 2 chunks (1996 + 104)
-- Tests: worst-case discrepancy (avg=1050 vs constant=1996, ~86% overestimate)
CREATE TABLE toast_narrow (
    id         serial PRIMARY KEY,
    payload    text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE toast_narrow ALTER COLUMN payload SET STORAGE EXTERNAL;

-- toast_aligned: 3992 B = exactly 2 × 1996 → 2 full chunks, no partial
-- Tests: zero discrepancy (avg=1996 = constant=1996)
CREATE TABLE toast_aligned (
    id         serial PRIMARY KEY,
    payload    text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE toast_aligned ALTER COLUMN payload SET STORAGE EXTERNAL;

-- toast_mixed: varying payload sizes (2.1 KB, 8 KB, 15 KB) within same table
-- Tests: sampling robustness — overall avg_chunk ≈ 1673, but depends on sampled value
CREATE TABLE toast_mixed (
    id         serial PRIMARY KEY,
    payload    text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE toast_mixed ALTER COLUMN payload SET STORAGE EXTERNAL;

-- toast_multi_col: 2 TOAST columns (3 KB + 2.5 KB)
-- Tests: multi-column TOAST estimation
CREATE TABLE toast_multi_col (
    id         serial PRIMARY KEY,
    col_a      text NOT NULL,
    col_b      text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE toast_multi_col ALTER COLUMN col_a SET STORAGE EXTERNAL;
ALTER TABLE toast_multi_col ALTER COLUMN col_b SET STORAGE EXTERNAL;

-- toast_compressed: 8 KB semi-compressible values, STORAGE EXTENDED (default)
-- Tests: pg_column_size() vs octet_length() — octet_length returns decompressed (8000),
-- pg_column_size returns compressed (~4.3 KB), only pg_column_size gives correct chunk avg
CREATE TABLE toast_compressed (
    id         serial PRIMARY KEY,
    payload    text NOT NULL,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

-- Keep default STORAGE EXTENDED (compression enabled, unlike other TOAST tables)

-- notifications: small TOAST table (< 10 MB → unreliable estimate)
CREATE TABLE notifications (
    id         serial PRIMARY KEY,
    user_id    integer NOT NULL,
    message    text NOT NULL,
    read       boolean DEFAULT false,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

ALTER TABLE notifications ALTER COLUMN message SET STORAGE EXTERNAL;

-- archive.events: separate schema for -n testing
CREATE TABLE archive.events (
    id         serial PRIMARY KEY,
    event_type varchar(50) NOT NULL,
    data       jsonb,
    created_at timestamptz NOT NULL
) WITH (autovacuum_enabled = false);

CREATE INDEX idx_events_type ON archive.events(event_type);

COMMIT;

-- ============================================================================
-- Data insertion (outside transaction for better progress visibility)
-- ============================================================================

\echo 'Inserting orders (500k rows)...'
INSERT INTO orders (customer_id, status, amount, created_at)
SELECT
    (random() * 10000)::int + 1,
    (ARRAY['pending','confirmed','shipped','delivered','cancelled'])[floor(random()*5+1)::int],
    (random() * 500 + 1)::numeric(10,2),
    now() - make_interval(days => (random()*365)::int, secs => (random()*86400)::int)
FROM generate_series(1, 500000);

\echo 'Inserting sessions (200k rows)...'
INSERT INTO sessions (user_id, token, ip_address, expires_at, created_at)
SELECT
    (random() * 5000)::int + 1,
    md5(random()::text) || md5(random()::text),
    ('192.168.' || (random()*255)::int || '.' || (random()*255)::int)::inet,
    now() + make_interval(hours => (random()*72)::int),
    now() - make_interval(days => (random()*30)::int, secs => (random()*86400)::int)
FROM generate_series(1, 200000);

\echo 'Inserting products (100k rows)...'
INSERT INTO products (name, description, price, category, updated_at)
SELECT
    'Product ' || g,
    'Description for product ' || g || '. ' || repeat('x', (random()*200)::int),
    (random() * 1000 + 0.5)::numeric(10,2),
    (ARRAY['electronics','clothing','food','books','toys','tools','sports','home'])[floor(random()*8+1)::int],
    now() - make_interval(days => (random()*180)::int)
FROM generate_series(1, 100000) g;

\echo 'Inserting customers (50k rows)...'
INSERT INTO customers (email, name, created_at)
SELECT
    'user' || g || '@example.com',
    'Customer ' || g,
    now() - make_interval(days => (random()*730)::int)
FROM generate_series(1, 50000) g;

\echo 'Inserting audit_log (10k rows with ~4 KB TOAST payload)...'
INSERT INTO audit_log (event_type, payload, created_at)
SELECT
    (ARRAY['login','logout','update','delete','create','export'])[floor(random()*6+1)::int],
    repeat(chr(65 + (g % 26)::int), 4000),
    now() - make_interval(days => (random()*90)::int, secs => (random()*86400)::int)
FROM generate_series(1, 10000) g;

\echo 'Inserting toast_large (3000 rows with ~20 KB payload)...'
INSERT INTO toast_large (payload, created_at)
SELECT
    repeat(chr(65 + (g % 26)::int), 20000),
    now() - make_interval(days => (random()*90)::int)
FROM generate_series(1, 3000) g;

\echo 'Inserting toast_narrow (6000 rows with 2100 B payload)...'
INSERT INTO toast_narrow (payload, created_at)
SELECT
    repeat(chr(65 + (g % 26)::int), 2100),
    now() - make_interval(days => (random()*90)::int)
FROM generate_series(1, 6000) g;

\echo 'Inserting toast_aligned (4000 rows with 3992 B payload)...'
INSERT INTO toast_aligned (payload, created_at)
SELECT
    repeat(chr(65 + (g % 26)::int), 3992),
    now() - make_interval(days => (random()*90)::int)
FROM generate_series(1, 4000) g;

\echo 'Inserting toast_mixed (9000 rows with 2.1/8/15 KB payloads)...'
-- 3 tiers: 2100 B (2 chunks), 8000 B (5 chunks), 15000 B (8 chunks)
INSERT INTO toast_mixed (payload, created_at)
SELECT
    repeat(chr(65 + (g % 26)::int),
           CASE WHEN g <= 3000 THEN 2100
                WHEN g <= 6000 THEN 8000
                ELSE 15000 END),
    now() - make_interval(days => (random()*90)::int)
FROM generate_series(1, 9000) g;

\echo 'Inserting toast_multi_col (3000 rows with 3 KB + 2.5 KB)...'
INSERT INTO toast_multi_col (col_a, col_b, created_at)
SELECT
    repeat(chr(65 + (g % 26)::int), 3000),
    repeat(chr(97 + (g % 26)::int), 2500),
    now() - make_interval(days => (random()*90)::int)
FROM generate_series(1, 3000) g;

\echo 'Inserting toast_compressed (5000 rows with 8 KB semi-compressible payload)...'
-- Semi-compressible: 4 KB repeated pattern + 4 KB pseudo-random (md5-based)
-- Compresses to ~4.3 KB stored, testing EXTENDED storage estimation
INSERT INTO toast_compressed (payload, created_at)
SELECT
    repeat(chr(65 + (g % 26)::int), 4000)
    || string_agg(md5((g * s)::text), '')
    AS payload,
    now() - make_interval(days => (random()*90)::int)
FROM generate_series(1, 5000) g,
     LATERAL generate_series(1, 125) s
GROUP BY g;

\echo 'Inserting notifications (500 rows with ~4 KB TOAST message)...'
INSERT INTO notifications (user_id, message, read, created_at)
SELECT
    sub.user_id,
    sub.message,
    random() > 0.5,
    now() - make_interval(days => (random()*30)::int)
FROM (
    SELECT
        g,
        (random() * 100)::int + 1 AS user_id,
        string_agg(md5(random()::text), '') AS message
    FROM generate_series(1, 500) g,
         LATERAL generate_series(1, 125) s  -- 125 × 32 chars = 4000 bytes
    GROUP BY g
) sub;

\echo 'Inserting archive.events (50k rows)...'
INSERT INTO archive.events (event_type, data, created_at)
SELECT
    (ARRAY['page_view','click','signup','purchase','error'])[floor(random()*5+1)::int],
    jsonb_build_object('user_id', (random()*5000)::int, 'page', '/page/' || g, 'duration_ms', (random()*5000)::int),
    now() - make_interval(days => (random()*365)::int, secs => (random()*86400)::int)
FROM generate_series(1, 50000) g;

-- ============================================================================
-- Create bloat by deleting rows (deterministic via modulo)
-- ============================================================================

\echo 'Creating bloat patterns...'

-- orders: delete 70% → CRITICAL heap bloat
DELETE FROM orders WHERE id % 10 >= 3;

-- sessions: delete 35% → HIGH heap bloat
DELETE FROM sessions WHERE id % 20 < 7;

-- products: delete 15% → MEDIUM heap bloat
DELETE FROM products WHERE id % 20 < 3;

-- customers: no delete → clean table

-- audit_log: delete 60% → CRITICAL TOAST bloat
DELETE FROM audit_log WHERE id % 5 >= 2;

-- toast_large: delete 50%
DELETE FROM toast_large WHERE id % 2 = 0;

-- toast_narrow: delete 40%
DELETE FROM toast_narrow WHERE id % 5 >= 3;

-- toast_aligned: delete 55%
DELETE FROM toast_aligned WHERE id % 20 < 11;

-- toast_mixed: delete 50%
DELETE FROM toast_mixed WHERE id % 2 = 0;

-- toast_multi_col: delete 45%
DELETE FROM toast_multi_col WHERE id % 20 < 9;

-- toast_compressed: delete 50%
DELETE FROM toast_compressed WHERE id % 2 = 0;

-- notifications: delete 50%
DELETE FROM notifications WHERE id % 2 = 0;

-- archive.events: delete 45% → HIGH heap bloat
DELETE FROM archive.events WHERE id % 20 < 9;

-- ============================================================================
-- VACUUM ANALYZE: reclaim dead tuples but NOT pages (bloat preserved)
-- ============================================================================

\echo 'Running VACUUM ANALYZE...'

VACUUM ANALYZE orders;
VACUUM ANALYZE sessions;
VACUUM ANALYZE products;
VACUUM ANALYZE customers;
VACUUM ANALYZE audit_log;
VACUUM ANALYZE toast_large;
VACUUM ANALYZE toast_narrow;
VACUUM ANALYZE toast_aligned;
VACUUM ANALYZE toast_mixed;
VACUUM ANALYZE toast_multi_col;
VACUUM ANALYZE toast_compressed;
VACUUM ANALYZE notifications;
VACUUM ANALYZE archive.events;

-- ============================================================================
-- Summary
-- ============================================================================

\echo ''
\echo 'Demo database ready. Table summary:'
\echo ''

SELECT
    n.nspname || '.' || c.relname AS "table",
    pg_size_pretty(pg_total_relation_size(c.oid)) AS "total_size",
    s.n_live_tup AS "live_rows",
    s.n_dead_tup AS "dead_rows"
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE c.relkind = 'r'
  AND n.nspname IN ('public', 'archive')
ORDER BY pg_total_relation_size(c.oid) DESC;
