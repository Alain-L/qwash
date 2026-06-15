# qwash

**qwash** is a PostgreSQL bloat analysis and reduction tool. It identifies and reduces table bloat **using regular DML operations** instead of a long exclusive lock (unlike `VACUUM FULL`), making it suitable for production use (see [Operational Caveats](#operational-caveats)).

qwash is a **standalone tool** that combines bloat estimation and reduction in a single binary:
- **No extensions required** — works with any PostgreSQL 9.6+ installation
- **No external dependencies** — no Perl, Python, or `pgstattuple` needed
- **Estimate then debloat** — analyze bloat first, then reduce it based on results

## Features

- **Bloat Estimation** — Analyze table, TOAST and B-Tree index bloat using PostgreSQL system catalogs (no `pgstattuple`)
- **Non-blocking Reduction** — Reclaim space incrementally; writes keep flowing during compaction
- **Trigger & FK Safe** — regular triggers and FK checks are suspended via `session_replication_role = replica` (own session only; `ENABLE ALWAYS`/`ENABLE REPLICA` triggers are the exception, see [Operational Caveats](#operational-caveats))
- **Multiple Modes** — Default (2 workers), fast (4 workers), or slow (1 worker with delay)
- **Dry-run Support** — Preview changes before applying them
- **JSON Output** — Machine-readable output for automation and monitoring
- **Limit Control** — Stop after reducing a specific amount of bloat

## Installation

### Pre-built binaries

Download the latest release from [GitHub Releases](https://github.com/Alain-L/qwash/releases):

```sh
VERSION=0.4.0  # Check latest version on GitHub

# Linux (amd64)
curl -LO https://github.com/Alain-L/qwash/releases/download/v${VERSION}/qwash_${VERSION}_linux_amd64.tar.gz
tar xzf qwash_${VERSION}_linux_amd64.tar.gz
sudo mv qwash /usr/local/bin/

# macOS (Apple Silicon)
curl -LO https://github.com/Alain-L/qwash/releases/download/v${VERSION}/qwash_${VERSION}_darwin_arm64.tar.gz
tar xzf qwash_${VERSION}_darwin_arm64.tar.gz
sudo mv qwash /usr/local/bin/
```

### Debian/Ubuntu

```sh
VERSION=0.4.0
curl -LO https://github.com/Alain-L/qwash/releases/download/v${VERSION}/qwash_${VERSION}_linux_amd64.deb
sudo dpkg -i qwash_${VERSION}_linux_amd64.deb
```

### RHEL/Rocky/Fedora

```sh
VERSION=0.4.0
curl -LO https://github.com/Alain-L/qwash/releases/download/v${VERSION}/qwash_${VERSION}_linux_amd64.rpm
sudo rpm -i qwash_${VERSION}_linux_amd64.rpm
```

### From source

```sh
git clone https://github.com/Alain-L/qwash.git
cd qwash
go build -o bin/qwash
```

### Requirements

- PostgreSQL 9.6+
- For `--debloat`: a superuser role (PostgreSQL < 15) or a role granted `SET` on `session_replication_role` (PostgreSQL 15+), which must also **own the target tables** so that `VACUUM` can reclaim the freed pages
- For `--debloat`: a direct connection (no transaction-pooling pgbouncer; qwash relies on session state)

## Quick Start

qwash follows the standard PostgreSQL client conventions: connection
parameters not given on the command line are resolved like psql would, from
`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE`,
`~/.pgpass` and the usual libpq defaults (local socket, OS user, `sslmode=prefer`).

### Estimate Bloat

```sh
# Analyze all tables in a database (heap bloat)
./bin/qwash --estimate -d mydb -U postgres -h localhost

# Analyze TOAST bloat
./bin/qwash --estimate --toast -d mydb

# Analyze both heap and TOAST bloat
./bin/qwash --estimate --heap --toast -d mydb

# Analyze B-Tree index bloat
./bin/qwash --estimate --btree -d mydb

# Analyze indexes of a specific table
./bin/qwash --estimate --btree -d mydb -t mytable

# Analyze specific tables
./bin/qwash --estimate -d mydb -t mytable -t othertable

# JSON output
./bin/qwash --estimate -d mydb --json
```

### Reduce Bloat

```sh
# Debloat specific tables (-t required, or use --all)
./bin/qwash --debloat -d mydb -t bloated_table

# Debloat multiple tables
./bin/qwash --debloat -d mydb -t table1 -t table2 -t table3

# Debloat every table in the database (explicit opt-in)
./bin/qwash --debloat -d mydb --all

# Fast mode (4 workers, 1 pass, ~97% efficiency)
./bin/qwash --debloat -d mydb -t mytable --fast

# Slow mode (1 worker, 3 passes, minimal server impact)
./bin/qwash --debloat -d mydb -t mytable --slow --delay 100

# Dry-run (preview without changes)
./bin/qwash --debloat -d mydb -t mytable --dry-run

# Stop after reducing 500MB of bloat
./bin/qwash --debloat -d mydb -t mytable --limit 500MB

# Rebuild indexes after debloat
./bin/qwash --debloat -d mydb -t mytable --reindex
```

## Command Reference

```
Usage:
  qwash [flags]

Connection:
  -d, --dbname string     Database name (default: PGDATABASE, or the user name)
  -U, --dbuser string     Database user (default: PGUSER, or the OS user)
  -h, --host string       Database host or socket directory (default: PGHOST, or local socket)
  -p, --port string       Database port (default: PGPORT, or 5432)
  -W, --password          Force password prompt (default: PGPASSWORD, or ~/.pgpass)
      --sslmode string    SSL mode: disable, allow, prefer, require, verify-ca, verify-full (default: PGSSLMODE, or prefer)

Analysis:
  -E, --estimate          Display bloat estimation report
      --heap              Analyze heap (table) bloat (default if no target specified)
      --toast             Analyze TOAST bloat
      --btree             Analyze B-Tree index bloat
  -D, --detail            Show detailed analysis per table and index (not yet implemented; use -t for a per-table view)
  -t, --table strings     Target specific table(s)
  -n, --schema strings    Target specific schema(s)
  -X, --exclude-table     Exclude specific tables
  -S, --system            Include system catalog tables (pg_catalog) in the estimate

Debloat:
  -B, --debloat           Perform bloat reduction
      --all               Debloat every table (required when -t is not given)
      --fast              Fast mode: 4 workers, 1 pass (~97% efficiency)
      --slow              Slow mode: 1 worker, 3 passes, with delay between pages
      --delay int         Delay in ms between pages in slow mode (default: 10)
  -j, --jobs int          Parallel workers (default: 2, 4 with --fast, 1 with --slow)
      --dry-run           Preview changes without applying them
      --reindex           Rebuild indexes after debloat (REINDEX CONCURRENTLY)
      --limit string      Stop after reducing X bloat (e.g., 500MB, 1GB, 50%)

Output:
  -v, --verbose           Enable verbose output
  -J, --json              Output in JSON format

Other:
  -T, --test-connection   Test database connection and exit
      --help              Show help
```

## How It Works

### What is Bloat?

In PostgreSQL, **bloat** is wasted space inside table files. It's not just about dead tuples (`n_dead_tup`).

Even after `VACUUM` removes dead tuples, pages may remain partially filled:
- Deleted rows leave gaps that new inserts may not perfectly fill
- Updates create new row versions, fragmenting data across pages
- `VACUUM` frees space *within* pages but doesn't move rows between pages
- Over time, pages become sparsely populated

**Example:** A table might show `n_dead_tup = 0` after VACUUM, yet still use 100 pages when the live data could fit in 60. Those 40 extra pages are bloat — they consume disk space and slow down sequential scans.

Only `VACUUM FULL` (or tools like qwash) can reclaim this space by rewriting the table more compactly.

### Bloat Estimation

qwash uses the [ioguix bloat estimation approach](https://github.com/ioguix/pgsql-bloat-estimation) to analyze PostgreSQL system catalogs (`pg_class`, `pg_stat_all_tables`, `pg_stats`) without requiring the `pgstattuple` extension. The query has been [reworked](sql/table_bloat.sql) to run standalone without temporary tables (it returns sizes in raw bytes; wrap the size columns in `pg_size_pretty(...)` to read it by hand). It compares:

- **Actual table size** (pages currently allocated)
- **Minimum required pages** (calculated from live tuple count and average tuple size)

The difference is the estimated bloat.

**TOAST bloat** (`--toast`) uses a similar approach: it compares actual TOAST pages with the theoretical minimum based on live chunk count and average chunk size measured directly on the TOAST chunks (no detoasting). Estimation is reliable for TOAST tables >= 10 MB and requires recent `VACUUM` for accurate stats. A [standalone query](sql/toast_bloat.sql) is available for DBA use without installing qwash.

**B-Tree index bloat** (`--btree`) follows the same ioguix methodology adapted for indexes: it derives the theoretical minimum number of pages from `pg_stats` (`avg_width`, `null_frac`) and B-Tree page overhead (page header, opaque, item pointers, tuple header, MAXALIGN padding) and compares it to the actual `relpages` count. Indexes whose key columns include a `name`-typed column are flagged as **unreliable** (`is_na = true`) because `pg_stats` returns inaccurate widths for that type. A [standalone query](sql/btree_bloat.sql) is also available for DBA use.

### Bloat Reduction Algorithm

The debloat algorithm is inspired by [pgcompacttable](https://github.com/dataegret/pgcompacttable) but uses an **UPDATE-based compaction** approach via a temporary stored procedure:

1. Create a procedure that updates rows from the last N pages (`UPDATE SET col = col`)
2. PostgreSQL rewrites these tuples, placing them in earlier free space (HOT updates are bypassed)
3. Run `VACUUM` to release the now-empty pages at the end
4. Repeat until bloat is minimized

This approach:
- **Lets writes keep flowing** — compaction uses regular `UPDATE`s (row-level locking only); the only exclusive lock is the brief one `VACUUM` takes to truncate empty pages at the end of the file
- **Is transaction-safe** — one page per transaction; interrupting qwash only loses the page in progress
- **Works incrementally** — progress is preserved between runs
- **Preserves row identity** — no DELETE/INSERT, sequences and references unchanged
- **Trigger & FK safe** — uses `session_replication_role = replica` on its own session only; other sessions are unaffected (see caveats below for the exceptions)

### Operational Caveats

qwash runs a preflight check on each table before compacting it: it **refuses**
a table whose pages it could not reclaim (the current role is neither the owner
nor a superuser) or whose `UPDATE`s would fail (in a publication without a
usable `REPLICA IDENTITY`), and **warns** about `ENABLE ALWAYS`/`REPLICA`
triggers and publication membership. The rest of the points below still
warrant attention:

- **Privileges** — requires superuser (PostgreSQL < 15) or `SET session_replication_role` privilege (15+), and **ownership of the target tables**: PostgreSQL silently skips `VACUUM` on tables you don't own, in which case moved rows are never reclaimed (qwash now refuses this case up front).
- **Locks** — `UPDATE`s take row-level locks on the rows being moved (concurrent application updates on those rows wait for the page transaction, which is short). `VACUUM`'s end-of-table truncation takes a **brief exclusive lock** and can cause recovery conflicts on hot standbys.
- **Triggers** — regular triggers don't fire, but `ENABLE ALWAYS` triggers (e.g. audit or `moddatetime` triggers) **still fire** on every moved row, and `ENABLE REPLICA` triggers **start firing** (qwash warns when such triggers exist). Review trigger definitions before debloating such tables.
- **WAL and logical replication** — every moved row is written to WAL (volume ≈ data moved) and **decoded by logical replication**: expect subscriber traffic and lag proportional to the bloat being removed (qwash warns when the table is published).
- **Connection pooling** — connect **directly** to PostgreSQL. Through a transaction-pooling pgbouncer, the session-level protections (`session_replication_role`, `lock_timeout`, advisory locks) may land on different backends and silently stop working.
- **Statistics matter** — the bloat estimation is based on `pg_stats`/`pg_class`; run `ANALYZE` (and ideally `VACUUM`) on the target tables first if their statistics are stale.
- **Interruptions** — `Ctrl-C` stops cleanly between pages: the page in progress rolls back, already-compacted pages stay. `--reindex` uses `REINDEX CONCURRENTLY` only (PostgreSQL 12+) and never falls back to a blocking `REINDEX`.

**Exit codes** (for automation): `0` success · `1` fatal error (bad flags, connection failure, unknown `-t` table) · `2` completed with per-table failures.

### Debloat Modes

| Mode | Workers | Passes | Efficiency | Use Case |
|------|---------|--------|------------|----------|
| **default** | 2 | 2 | ~99% | Balanced for most workloads |
| **fast** | 4 | 1 | ~97% | When speed matters more than perfection |
| **slow** | 1 | 3 | ~99-100% | Minimal impact on production (with `--delay`) |

## Output Examples

### Text Output (--estimate)

```
qwash – 5 tables analyzed

SUMMARY

  Tables analyzed           : 5
  Tables with bloat         : 5 (100.0%)

  Total database size       : 23.4 MB
  Total bloat detected      : 12.3 MB (52.4%)
  Reclaimable space         : 12.3 MB

CRITICAL BLOAT (≥ 50%)

  Table                                            Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.orders                                 12.0 MB       8.8 MB     71.95%
  public.audit_log                             296.0 KB     176.0 KB     59.46%
  public.notifications                          16.0 KB       8.0 KB     50.00%

  Total: 3 tables | 9.0 MB bloat reclaimable

HIGH BLOAT (30-50%)

  Table                                            Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.sessions                                8.1 MB       2.8 MB     35.11%

  Total: 1 tables | 2.8 MB bloat reclaimable

MEDIUM BLOAT (10-30%)

  Table                                            Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.products                                3.1 MB     440.0 KB     13.99%

  Total: 1 tables | 440.0 KB bloat reclaimable
```

### Text Output (--estimate --toast)

```
qwash – 3 tables with TOAST analyzed

TOAST BLOAT SUMMARY

  Tables analyzed           : 3
  Tables with bloat         : 2 (66.7%)

  Total TOAST size          : 130.2 MB
  Total bloat detected      : 69.3 MB (53.2%)
  Reclaimable space         : 69.3 MB

CRITICAL BLOAT (≥ 50%)

  Table                                      TOAST Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.audit_log                              52.1 MB      36.3 MB     69.60%
  public.toast_large                            62.5 MB      33.1 MB     53.00%

  Total: 2 tables | 69.3 MB bloat reclaimable

UNRELIABLE ESTIMATES (< 10 MB)

  Table                                      TOAST Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.notifications                           2.6 MB          N/A          -
```

TOAST bloat estimation requires recent `VACUUM` (not just `ANALYZE`) for accurate `pg_class` stats. Tables with TOAST data smaller than 10 MB are flagged as unreliable.

### Text Output (--estimate --btree)

```
qwash – 12 B-Tree indexes analyzed

INDEX BLOAT SUMMARY

  Indexes analyzed          : 12
  Indexes with bloat        : 7 (58.3%)

  Total index size          : 84.0 MB
  Total bloat detected      : 31.7 MB (37.7%)
  Reclaimable space         : 31.7 MB

CRITICAL BLOAT (≥ 50%)

  Index                          Table                                Size      Bloat    Bloat %
  ------------------------------------------------------------------------------------------------
  public.orders_customer_idx     public.orders                     12.0 MB     7.2 MB     60.0%
  public.audit_log_pkey          public.audit_log                   8.0 MB     4.4 MB     55.0%

  Total: 2 indexes | 11.6 MB bloat reclaimable

HIGH BLOAT (30-50%)

  Index                          Table                                Size      Bloat    Bloat %
  ------------------------------------------------------------------------------------------------
  public.sessions_user_idx       public.sessions                    8.0 MB     3.2 MB     40.0%

  Total: 1 indexes | 3.2 MB bloat reclaimable

UNRELIABLE ESTIMATES (is_na = true)

  Index                          Table                                Size      Bloat    Bloat %
  ------------------------------------------------------------------------------------------------
  pg_catalog.pg_class_relname    pg_catalog.pg_class              512.0 KB        N/A          -

  Columns of type "name" produce unreliable pg_stats estimates.
```

### Text Output (--estimate --btree -t table)

```
INDEX BLOAT ESTIMATION

public.orders_customer_idx

  Table       : public.orders
  Size        : 12.0 MB
  Bloat       : 7.2 MB
  Bloat %     : 60.0%
  Pages       : 1572
  Min pages   : 629
  Bloat pages : 943
  Fill factor : 90
```

### JSON Output (--estimate --btree --json)

```json
{
  "indexes": [
    {
      "schema": "public",
      "index_name": "orders_customer_idx",
      "table_name": "orders",
      "index_size": 12582912,
      "pages": 1572,
      "min_pages": 629,
      "bloat_pages": 943,
      "bloat_size": 7544832,
      "bloat_ratio": 60.0,
      "bloat_pct": 60.0,
      "fill_factor": 90
    }
  ]
}
```

### Text Output (--estimate -t table)

```
BLOAT ESTIMATION

public.orders

  Size        : 12.0 MB
  Bloat       : 8.8 MB
  Bloat %     : 71.95%
  Pages       : 1572
  Min pages   : 441
  Live tuples : 60000
  Dead tuples : 0
  Fill factor : 100
```

### JSON Output (--estimate --json)

```json
{
  "tables": [
    {
      "schema": "public",
      "table_name": "orders",
      "table_size": 12582912,
      "bloat_size": 9265152,
      "bloat_ratio": 71.95,
      "pages": 1572,
      "min_pages": 441,
      "live_tuples": 60000,
      "dead_tuples": 0,
      "fill_factor": 100
    }
  ]
}
```

### JSON Output (--estimate --toast --json)

```json
{
  "toast": [
    {
      "schema": "public",
      "table_name": "audit_log",
      "toast_size": 54616064,
      "toast_pages": 6667,
      "toast_chunks": 12000,
      "bloat_pct": 69.6,
      "bloat_size": 38020064
    }
  ]
}
```

### JSON Output (--debloat --json)

```json
{
  "summary": {
    "tables_processed": 1,
    "tables_compacted": 1,
    "mode": "default",
    "total_pages_removed": 18,
    "total_bytes_removed": 147456,
    "duration_ms": 1250
  },
  "results": [
    {
      "table": "orders",
      "initial_pages": 37,
      "final_pages": 19,
      "bloat_removed_pages": 18,
      "bloat_removed_bytes": 147456,
      "duration_ms": 1250
    }
  ]
}
```

## Testing

```sh
# Run all tests (requires PostgreSQL)
PGUSER=myuser PGPASSWORD=mypass go test ./tests -v

# Run only golden file tests
go test ./tests -run TestGolden -v

# Run only estimate tests
go test ./tests -run TestEstimate -v
```

## Comparison with Alternatives

| Feature | VACUUM FULL | pg_repack | pg_squeeze | pgcompacttable | **qwash** |
|---------|-------------|-----------|------------|----------------|-----------|
| Non-blocking | No | Yes | Yes | Yes | **Yes** |
| No extension | Yes | No | No | No² | **Yes** |
| No server config | Yes | Yes | No¹ | Yes | **Yes** |
| No dependencies | Yes | Yes | Yes | No³ | **Yes** |
| In-place (no 2x space) | No | No | No | Yes | **Yes** |
| Incremental | No | No | No | Yes | **Yes** |
| Trigger safe | Yes | No | Yes | Yes | **Yes** |
| FK safe | Yes | No | Yes | Yes | **Yes** |
| Built-in estimation | No | No | No | No | **Yes** |
| Parallel workers | No | No | Yes | No | **Yes** |

¹ pg_squeeze requires `wal_level=logical` and `shared_preload_libraries`

² pgcompacttable requires the `pgstattuple` extension

³ pgcompacttable requires Perl with `DBD::Pg`

**qwash** is the only tool that combines non-blocking operation, no extensions, no server configuration, and minimal disk space in a single binary.

## References

- [ioguix/pgsql-bloat-estimation](https://github.com/ioguix/pgsql-bloat-estimation) — Approach for stats-based bloat estimation without pgstattuple
- [dataegret/pgcompacttable](https://github.com/dataegret/pgcompacttable) — Perl tool for reorganizing bloated tables without locks

## License

PostgreSQL License. See [LICENSE.md](LICENSE.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
