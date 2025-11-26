-- Demo
DROP TABLE demo_qwash;

CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT i AS id
FROM generate_series(1,10000) i;

SELECT pg_relation_size('demo_qwash'); --  65536 = 8 * 8192 -- 8 blocs

-- PostgreSQL a alloué 8 blocs

-- On vérifie le nombre de lignes par blocs :
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
--     4 |        96 -- not full
-- (5 lignes)

-- Il manque 3 blocs ! la table est bloatée dès sa création
VACUUM; -- pour élaguer les blocs alloués en trop
SELECT pg_relation_size('demo_qwash'); --  40960 = 5 * 8192 -- 5 blocs

-- make some bloat

DELETE FROM demo_qwash WHERE id <= 500;

-- ce qui reste 
 page | row_count 
------+-----------
    2 |       178
    3 |       226
    4 |        96

-- pg_relation_size : 40960 

VACUUM;
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS SELECT * FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (3,4); -- les deux ctid les plus élevés
DELETE FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (3,4);
INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

fin 1ère boucle :
 page | row_count 
------+-----------
    0 |       226
    1 |        96
    2 |       178
(3 lignes)
-- les 500 lignes sont bien dans les 3 premiers blocs, on pourrait s'arrêter là.
-- L'espace n'a pas encore été récupéré

-- pg_relation_size : 24576  

-- Après ANALYZE;
--    table_name     | live_tup | dead_tup | fillfactor | relation_size | TOAST_size | bloat_size | bloat_pct 
---------------------+----------+----------+------------+---------------+------------+------------+-----------
-- public.demo_qwash |      500 |        0 |        100 | 24 kB         | N/A        | 0 bytes    |      0.00


-- Démo 2 

-- création table
DROP TABLE demo_qwash;
CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT
  i AS id,
  substring(md5(random()::text) FROM 1 FOR (5 + floor(random() * 10))::int) AS random_string
FROM generate_series(1, 1000) i;

-- taille avant VACUUM
SELECT pg_relation_size('demo_qwash'); --  65536 = 8 * 8192 -- 8 blocs

-- taille après VACUUM
VACUUM;
SELECT pg_relation_size('demo_qwash'); --  49152 = 6 * 8192 -- 6 blocs

-- inspection des blocs : 
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
--     5 |       118
-- le dernier est moins rempli, les précédents n'ont pas le même nombre de lignes
ANALYZE ;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 1000
-- dead_tup           | 0
-- min pages required | 6
-- actual pages       | 6
-- fillfactor         | 100
-- relation_size      | 48 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 0 bytes
-- bloat_pct          | 0.00
-- pas de bloat, 6 blocs nécessaires


-- faisons du bloat
DELETE FROM demo_qwash WHERE id <= 250;
DELETE FROM demo_qwash WHERE id >= 500 AND id < 750;

-- état des blocs 
--  page | row_count 
-- ------+-----------
--     1 |       103
--     2 |       146
--     4 |       133
--     5 |       118
-- des blocs disparus...

-- taille après VACUUM
VACUUM;
SELECT pg_relation_size('demo_qwash'); --  49152 = 6 * 8192 -- 6 blocs
-- mais toujours la même taille

ANALYZE ;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 500
-- dead_tup           | 557
-- min pages required | 3
-- actual pages       | 6
-- fillfactor         | 100
-- relation_size      | 48 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 24 kB
-- bloat_pct          | 50.00
-- on a bien 50% de bloat
-- 3 pages suffissent

-- boucle qwash
VACUUM;
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS SELECT * FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (4,5); -- les deux ctid les plus élevés
DELETE FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (4,5);
INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

-- état des blocs 
--  page | row_count 
-- ------+-----------
--     0 |       178
--     1 |       176
--     2 |       146
-- redoutable ! les lignes ont bien été réécrites séquentiellement depuis le début

pas de boucle supplémentaire nécessaire

VACUUM;
-- taille après VACUUM
SELECT pg_relation_size('demo_qwash'); --  24576 = 3 * 8192 -- 3 blocs

ANALYZE;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 500
-- dead_tup           | 0
-- min pages required | 3
-- actual pages       | 3
-- fillfactor         | 100
-- relation_size      | 24 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 0 bytes
-- bloat_pct          | 0.00
-- 3 pages sur 3 tout est nickel


-- Démo 3

-- création table
DROP TABLE demo_qwash;
CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT
  i AS id,
  substring(md5(random()::text) FROM 1 FOR (5 + floor(random() * 10))::int) AS random_string
FROM generate_series(1, 5000) i; -- 5000 lignes

-- taille avant VACUUM
SELECT pg_relation_size('demo_qwash'); --  262144 = 32 * 8192 -- 32 blocs

-- taille après VACUUM
VACUUM;
SELECT pg_relation_size('demo_qwash'); --  237568 = 6 * 8192 -- 29 blocs

-- inspection des blocs : 
SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page,
       count(*) AS row_count
FROM public.demo_qwash
GROUP BY page
ORDER BY page;

--  page | row_count 
-- ------+-----------
--  page | row_count 
-- ------+-----------
--     0 |       174
--     1 |       177
--     2 |       174
--     3 |       176
--     4 |       176
--     5 |       175
--     6 |       176
--     7 |       175
--     8 |       176
--     9 |       174
--    10 |       174
--    11 |       173
--    12 |       176
--    13 |       176
--    14 |       175
--    15 |       175
--    16 |       176
--    17 |       175
--    18 |       175
--    19 |       177
--    20 |       175
--    21 |       176
--    22 |       175
--    23 |       173
--    24 |       177
--    25 |       177
--    26 |       175
--    27 |       176
--    28 |        91
-- le dernier est moins rempli, les précédents n'ont pas le même nombre de lignes

ANALYZE ;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 5000
-- dead_tup           | 371
-- min pages required | 27
-- actual pages       | 29
-- fillfactor         | 100
-- relation_size      | 232 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 16 kB
-- bloat_pct          | 6.90
-- bloat estimé marginal (imprécision ANALYZE, espace dans les blocs...)


-- faisons du bloat
DELETE FROM demo_qwash WHERE id <= 1000;
DELETE FROM demo_qwash WHERE id > 2500 AND id <= 4000;

-- état des blocs 
--  page | row_count 
-- ------+-----------
--     5 |        53
--     6 |       176
--     7 |       175
--     8 |       177
--     9 |       174
--    10 |       175
--    11 |       174
--    12 |       176
--    13 |       174
--    14 |        46
--    22 |        31
--    23 |       176
--    24 |       175
--    25 |       178
--    26 |       176
--    27 |       174
--    28 |        90
-- des blocs disparus, d'autres se sont vidés peu rempli...

-- taille après VACUUM
VACUUM;
SELECT pg_relation_size('demo_qwash'); --  237568 = 29 * 8192 -- 29 blocs
-- mais toujours la même taille

ANALYZE ;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 2500
-- dead_tup           | 2785
-- min pages required | 14
-- actual pages       | 29
-- fillfactor         | 100
-- relation_size      | 232 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 120 kB
-- bloat_pct          | 51.72
-- on a bien ~50% de bloat
-- 14 pages pourraient suffire

-- boucle qwash 1
VACUUM;
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS SELECT * FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (27,28); -- les deux ctid les plus élevés
DELETE FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (27,28);
INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

-- état des blocs 
--  page | row_count 
-- ------+-----------
--     0 |       174
--     1 |        90
--     5 |        53
--     6 |       176
--     7 |       175
--     8 |       177
--     9 |       174
--    10 |       175
--    11 |       174
--    12 |       176
--    13 |       174
--    14 |        46
--    22 |        31
--    23 |       176
--    24 |       175
--    25 |       178
--    26 |       176
-- Retour des blocs 0 et 1 !

VACUUM;
-- taille après VACUUM
SELECT pg_relation_size('demo_qwash'); --  24576 = 27 * 8192 -- 27 blocs

ANALYZE;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 2500
-- dead_tup           | 2413
-- min pages required | 14
-- actual pages       | 27
-- fillfactor         | 100
-- relation_size      | 216 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 104 kB
-- bloat_pct          | 48.15
-- l'estimation a baissé, on continue !

-- boucle qwash 2
-- VACUUM;
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS SELECT * FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (25,26); -- les deux ctid les plus élevés
DELETE FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (25,26);
INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

-- état des blocs 
--  page | row_count 
-- ------+-----------
--     0 |       174
--     1 |       175
--     2 |       177
--     3 |        92
--     5 |        53
--     6 |       176
--     7 |       175
--     8 |       177
--     9 |       174
--    10 |       175
--    11 |       174
--    12 |       176
--    13 |       174
--    14 |        46
--    22 |        31
--    23 |       176
--    24 |       175
-- Retour des blocs 2 et 3 !

ANALYZE;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 2500
-- dead_tup           | 2042
-- min pages required | 14
-- actual pages       | 25
-- fillfactor         | 100
-- relation_size      | 200 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 88 kB
-- bloat_pct          | 44.00
-- ça baisse encore !! on continue

-- boucle qwash 3
-- VACUUM;
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS SELECT * FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (23,24); -- les deux ctid les plus élevés
DELETE FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (23,24);
INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;

-- état des blocs 
--  page | row_count 
-- ------+-----------
--     0 |       174
--     1 |       175
--     2 |       177
--     3 |       176
--     4 |       176
--     5 |       144
--     6 |       176
--     7 |       175
--     8 |       177
--     9 |       174
--    10 |       175
--    11 |       174
--    12 |       176
--    13 |       174
--    14 |        46
--    22 |        31
-- On y est presque !

VACUUM;
SELECT pg_relation_size('demo_qwash'); --  188416 = 27 * 8192 -- 23 blocs

ANALYZE;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 2500
-- dead_tup           | 1671
-- min pages required | 14
-- actual pages       | 23
-- fillfactor         | 100
-- relation_size      | 184 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 72 kB
-- bloat_pct          | 39.13
-- ça baisse encore !! on continue


-- boucle qwash 4
-- VACUUM;
BEGIN;
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS SELECT * FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (21,22); -- les deux ctid les plus élevés
DELETE FROM demo_qwash WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN (21,22);
INSERT INTO demo_qwash SELECT * FROM qwash;
COMMIT;


-- état des blocs 
--  page | row_count 
-- ------+-----------
--     0 |       174
--     1 |       175
--     2 |       177
--     3 |       176
--     4 |       176
--     5 |       175
--     6 |       176
--     7 |       175
--     8 |       177
--     9 |       174
--    10 |       175
--    11 |       174
--    12 |       176
--    13 |       174
--    14 |        46
-- Qwash on fire !!!!

VACUUM;
SELECT pg_relation_size('demo_qwash'); --  122880 = 27 * 8192 -- 23 blocs

ANALYZE;
-- requête bloat :
-- -[ RECORD 1 ]------+------------------
-- table_name         | public.demo_qwash
-- live_tup           | 2500
-- dead_tup           | 186
-- min pages required | 14
-- actual pages       | 15
-- fillfactor         | 100
-- relation_size      | 120 kB
-- TOAST_size         | 0 bytes
-- bloat_size         | 8192 bytes
-- bloat_pct          | 6.67
-- bloat minimal, iso VACUUM FULL.



DROP TABLE demo_qwash;
CREATE TABLE demo_qwash
WITH (autovacuum_enabled = false)
AS
SELECT
  i AS id,
  substring(md5(random()::text) FROM 1 FOR (5 + floor(random() * 10))::int) AS random_string
FROM generate_series(1, 5000) i; -- 5000 lignes
VACUUM;
ANALYZE ;
DELETE FROM demo_qwash WHERE id <= 1000;
DELETE FROM demo_qwash WHERE id > 2500 AND id <= 4000;
