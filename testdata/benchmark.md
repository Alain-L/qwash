## Benchmark

## Built tables

```sql
                          Liste des relations
 Schéma |                  Nom                   | Type  | Propriétaire 
--------+----------------------------------------+-------+--------------
 public | bloated_high_bloat_table_300x          | table | alain
 public | bloated_low_bloat_table_300x           | table | alain
 public | bloated_medium_bloat_table_300x        | table | alain
 public | pgcompacttable_high_bloat_table_300x   | table | alain
 public | pgcompacttable_low_bloat_table_300x    | table | alain
 public | pgcompacttable_medium_bloat_table_300x | table | alain
 public | qwash_high_bloat_table_300x            | table | alain
 public | qwash_low_bloat_table_300x             | table | alain
 public | qwash_medium_bloat_table_300x          | table | alain
 public | vacuumfull_high_bloat_table_300x       | table | alain
 public | vacuumfull_low_bloat_table_300x        | table | alain
 public | vacuumfull_medium_bloat_table_300x     | table | alain
(12 lignes)
```

## Initial Bloat

```sql
                  table_name                   | live_tup | dead_tup | min_pages_required | actual_pages | fillfactor | relation_size | bloat_size | bloat_pct 
-----------------------------------------------+----------+----------+--------------------+--------------+------------+---------------+------------+-----------
 public.bloated_high_bloat_table_300x          |    90000 |   204975 |               1279 |         4190 |        100 | 33 MB         | 23 MB      |     69.47
 public.qwash_high_bloat_table_300x            |    90000 |   204975 |               1279 |         4190 |        100 | 33 MB         | 23 MB      |     69.47
 public.vacuumfull_high_bloat_table_300x       |    90000 |   204975 |               1279 |         4190 |        100 | 33 MB         | 23 MB      |     69.47
 public.pgcompacttable_high_bloat_table_300x   |    90000 |   204975 |               1279 |         4190 |        100 | 33 MB         | 23 MB      |     69.47
 public.qwash_medium_bloat_table_300x          |   210000 |    84989 |               2983 |         4190 |        100 | 33 MB         | 9656 kB    |     28.81
 public.vacuumfull_medium_bloat_table_300x     |   210000 |    84989 |               2983 |         4190 |        100 | 33 MB         | 9656 kB    |     28.81
 public.pgcompacttable_medium_bloat_table_300x |   210000 |    84989 |               2983 |         4190 |        100 | 33 MB         | 9656 kB    |     28.81
 public.bloated_medium_bloat_table_300x        |   210000 |    84989 |               2983 |         4190 |        100 | 33 MB         | 9656 kB    |     28.81
 public.vacuumfull_low_bloat_table_300x        |   270000 |    24997 |               3835 |         4190 |        100 | 33 MB         | 2840 kB    |      8.47
 public.bloated_low_bloat_table_300x           |   270000 |    24997 |               3835 |         4190 |        100 | 33 MB         | 2840 kB    |      8.47
 public.qwash_low_bloat_table_300x             |   270000 |    24997 |               3835 |         4190 |        100 | 33 MB         | 2840 kB    |      8.47
 public.pgcompacttable_low_bloat_table_300x    |   270000 |    24997 |               3835 |         4190 |        100 | 33 MB         | 2840 kB    |      8.47
(12 lignes)
```

## debloat with pgcompacttable

```console
./pgcompacttable \
  --host=localhost \
  --port=5437 \
  --user=alain \
  --dbname=qwash_tests \
  --tables-like='pgcompacttable_%' \
  --print-reindex-queries \
  --no-reindex \
  --verbose \
  --force

[Sun Mar 23 21:41:56 2025] (qwash_tests) Connecting to database
[Sun Mar 23 21:41:56 2025] (qwash_tests) Postgres backend pid: 82873
[Sun Mar 23 21:41:56 2025] (qwash_tests) It is recommended to set ionice -c 3 for pgcompacttable: ionice -c 3 -p 82873
[Sun Mar 23 21:41:56 2025] (qwash_tests) Handling tables. Attempt 1
[Sun Mar 23 21:41:56 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Start handling table public.pgcompacttable_high_bloat_table_300x
[Sun Mar 23 21:41:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Vacuum initial: 4190 pages left, duration 0.044 seconds.
[Sun Mar 23 21:41:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Bloat statistics with pgstattuple: duration 0.027 seconds.
[Sun Mar 23 21:41:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Statistics: 4190 pages (5020 pages including toasts and indexes), it is expected that ~68.660% (2876 pages) can be compacted with the estimated space saving being 22.474MB.
[Sun Mar 23 21:41:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Update by column: col5.
[Sun Mar 23 21:41:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Set pages/round: 5.
[Sun Mar 23 21:41:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Set pages/vacuum: 262.
[Sun Mar 23 21:42:57 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Progress: 91%,  2635 pages completed.
[Sun Mar 23 21:43:04 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Vacuum final: 1260 pages left, duration 1.052 seconds.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Analyze final: duration 0.089 second.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Bloat statistics with pgstattuple: duration 0.008 seconds.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Reindex queries: public.pgcompacttable_high_bloat_table_300x_pkey, initial size 825 pages (6.445MB), will be reduced by 69% (4.496MB)
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) CREATE UNIQUE INDEX CONCURRENTLY pgcompact_index_82872 ON public.pgcompacttable_high_bloat_table_300x USING btree (id) TABLESPACE pg_default; --qwash_tests
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) BEGIN; SET LOCAL statement_timeout TO 1000;
ALTER TABLE "public"."pgcompacttable_high_bloat_table_300x" DROP CONSTRAINT "pgcompacttable_high_bloat_table_300x_pkey";
ALTER TABLE "public"."pgcompacttable_high_bloat_table_300x" ADD CONSTRAINT "pgcompacttable_high_bloat_table_300x_pkey" PRIMARY KEY USING INDEX pgcompact_index_82872;
END;; --qwash_tests
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Processing complete.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Processing results: 1260 pages left (2089 pages including toasts and indexes), size reduced by 22.891MB (22.891MB including toasts and indexes) in total.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_high_bloat_table_300x) Finish handling table public.pgcompacttable_high_bloat_table_300x
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Start handling table public.pgcompacttable_low_bloat_table_300x
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Vacuum initial: 4190 pages left, duration 0.129 seconds.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Bloat statistics with pgstattuple: duration 0.014 seconds.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Statistics: 4190 pages (5020 pages including toasts and indexes), it is expected that ~10.200% (427 pages) can be compacted with the estimated space saving being 3.340MB.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Skipping processing: 10.20% space to compact from 20% minimum required.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Skipping reindex: public.pgcompacttable_low_bloat_table_300x_pkey, 9% space to compact from 20% minimum required.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_low_bloat_table_300x) Finish handling table public.pgcompacttable_low_bloat_table_300x
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Start handling table public.pgcompacttable_medium_bloat_table_300x
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Vacuum initial: 4190 pages left, duration 0.106 seconds.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Bloat statistics with pgstattuple: duration 0.012 seconds.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Statistics: 4190 pages (5020 pages including toasts and indexes), it is expected that ~29.550% (1238 pages) can be compacted with the estimated space saving being 9.674MB.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Update by column: col5.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Set pages/round: 5.
[Sun Mar 23 21:43:05 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Set pages/vacuum: 262.
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Vacuum final: 2935 pages left, duration 1.071 seconds.
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Analyze final: duration 0.072 second.
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Bloat statistics with pgstattuple: duration 0.011 seconds.
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Reindex queries: public.pgcompacttable_medium_bloat_table_300x_pkey, initial size 1034 pages (8.078MB), will be reduced by 43% (3.553MB)
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) CREATE UNIQUE INDEX CONCURRENTLY pgcompact_index_82872 ON public.pgcompacttable_medium_bloat_table_300x USING btree (id) TABLESPACE pg_default; --qwash_tests
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) BEGIN; SET LOCAL statement_timeout TO 1000;
ALTER TABLE "public"."pgcompacttable_medium_bloat_table_300x" DROP CONSTRAINT "pgcompacttable_medium_bloat_table_300x_pkey";
ALTER TABLE "public"."pgcompacttable_medium_bloat_table_300x" ADD CONSTRAINT "pgcompacttable_medium_bloat_table_300x_pkey" PRIMARY KEY USING INDEX pgcompact_index_82872;
END;; --qwash_tests
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Processing complete.
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Processing results: 2935 pages left (3973 pages including toasts and indexes), size reduced by 9.805MB (8.172MB including toasts and indexes) in total.
[Sun Mar 23 21:43:18 2025] (qwash_tests:public.pgcompacttable_medium_bloat_table_300x) Finish handling table public.pgcompacttable_medium_bloat_table_300x
[Sun Mar 23 21:43:18 2025] (qwash_tests) Processing complete.
[Sun Mar 23 21:43:18 2025] (qwash_tests) Processing results: size reduced by 32.695MB (31.055MB including toasts and indexes) in total.
[Sun Mar 23 21:43:18 2025] (qwash_tests) Disconnecting from database
[Sun Mar 23 21:43:18 2025] Processing complete: 1 retries to process has been done
[Sun Mar 23 21:43:18 2025] Processing results: size reduced by 32.695MB (31.055MB including toasts and indexes) in total, 32.695MB (31.055MB) qwash_tests.
```
