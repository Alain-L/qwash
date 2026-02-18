# qwash

**qwash** is a PostgreSQL bloat analysis and reduction tool. It identifies and reduces table bloat **without blocking writes** (unlike `VACUUM FULL`), making it safe for production use.

qwash is a **standalone tool** that combines bloat estimation and reduction in a single binary:
- **No extensions required** — works with any PostgreSQL 9.6+ installation
- **No external dependencies** — no Perl, Python, or `pgstattuple` needed
- **Estimate then debloat** — analyze bloat first, then reduce it based on results

## Features

- **Bloat Estimation** — Analyze table and TOAST bloat using PostgreSQL system catalogs (no `pgstattuple`)
- **Non-blocking Reduction** — Reclaim space incrementally without exclusive locks
- **Trigger & FK Safe** — uses `session_replication_role = replica` (own session only)
- **Multiple Modes** — Default (2 workers), fast (4 workers), or slow (1 worker with delay)
- **Dry-run Support** — Preview changes before applying them
- **JSON Output** — Machine-readable output for automation and monitoring
- **Limit Control** — Stop after reducing a specific amount of bloat

## Installation

### Pre-built binaries

Download the latest release from [GitHub Releases](https://github.com/Alain-L/qwash/releases):

```sh
VERSION=0.2.0  # Check latest version on GitHub

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
VERSION=0.2.0
curl -LO https://github.com/Alain-L/qwash/releases/download/v${VERSION}/qwash_${VERSION}_amd64.deb
sudo dpkg -i qwash_${VERSION}_amd64.deb
```

### RHEL/Rocky/Fedora

```sh
VERSION=0.2.0
curl -LO https://github.com/Alain-L/qwash/releases/download/v${VERSION}/qwash_${VERSION}_amd64.rpm
sudo rpm -i qwash_${VERSION}_amd64.rpm
```

### From source

```sh
git clone https://github.com/Alain-L/qwash.git
cd qwash
go build -o bin/qwash
```

### Requirements

- PostgreSQL 9.6+

## Quick Start

### Estimate Bloat

```sh
# Analyze all tables in a database (heap bloat)
./bin/qwash --estimate -d mydb -U postgres -H localhost

# Analyze TOAST bloat
./bin/qwash --estimate --toast -d mydb

# Analyze both heap and TOAST bloat
./bin/qwash --estimate --heap --toast -d mydb

# Analyze specific tables
./bin/qwash --estimate -d mydb -t mytable -t othertable

# JSON output
./bin/qwash --estimate -d mydb --json
```

### Reduce Bloat

```sh
# Debloat specific tables (required: -t flag)
./bin/qwash --debloat -d mydb -t bloated_table

# Debloat multiple tables
./bin/qwash --debloat -d mydb -t table1 -t table2 -t table3

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
  -d, --dbname strings    Target database(s)
  -U, --dbuser strings    Database user(s)
  -H, --host strings      Database host(s) (default: localhost)
  -P, --port strings      Database port(s) (default: 5432)
  -W, --password string   Database password
      --sslmode string    SSL mode: disable, require, verify-ca, verify-full (default: disable)

Analysis:
  -E, --estimate          Display bloat estimation report
      --heap              Analyze heap (table) bloat (default if neither --heap nor --toast)
      --toast             Analyze TOAST bloat
  -D, --detail            Show detailed analysis per table and index
  -t, --table strings     Target specific table(s)
  -n, --schema strings    Target specific schema(s)
  -X, --exclude-table     Exclude specific tables
  -S, --system            Include system tables (pg_catalog, information_schema)

Debloat:
  -B, --debloat           Perform bloat reduction
      --fast              Fast mode: 4 workers, 1 pass (~97% efficiency)
      --slow              Slow mode: 1 worker, 3 passes, with delay between pages
      --delay int         Delay in ms between pages in slow mode (default: 10)
      --dry-run           Preview changes without applying them
      --reindex           Rebuild indexes after debloat (REINDEX CONCURRENTLY)
      --limit string      Stop after reducing X bloat (e.g., 500MB, 1GB, 50%)

Output:
  -v, --verbose           Enable verbose output
  -J, --json              Output in JSON format

Other:
  -T, --test-connection   Test database connection and exit
  -h, --help              Show help
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

qwash uses the [ioguix bloat estimation approach](https://github.com/ioguix/pgsql-bloat-estimation) to analyze PostgreSQL system catalogs (`pg_class`, `pg_stat_user_tables`, `pg_stats`) without requiring the `pgstattuple` extension. The query has been [reworked](sql/table_bloat.sql) to run standalone without temporary tables. It compares:

- **Actual table size** (pages currently allocated)
- **Minimum required pages** (calculated from live tuple count and average tuple size)

The difference is the estimated bloat.

**TOAST bloat** (`--toast`) uses a similar approach: it compares actual TOAST pages with the theoretical minimum based on live chunk count and `TOAST_MAX_CHUNK_SIZE`. Estimation is reliable for TOAST tables >= 10 MB and requires recent `VACUUM` for accurate stats. See [sql/toast_bloat.sql](sql/toast_bloat.sql) for the standalone query.

### Bloat Reduction Algorithm

The debloat algorithm is inspired by [pgcompacttable](https://github.com/dataegret/pgcompacttable) but uses an **UPDATE-based compaction** approach via a temporary stored procedure:

1. Create a procedure that updates rows from the last N pages (`UPDATE SET col = col`)
2. PostgreSQL rewrites these tuples, placing them in earlier free space (HOT updates are bypassed)
3. Run `VACUUM` to release the now-empty pages at the end
4. Repeat until bloat is minimized

This approach:
- **Never blocks writes** — uses regular DML operations
- **Is transaction-safe** — can be interrupted safely
- **Works incrementally** — progress is preserved between runs
- **Preserves row identity** — no DELETE/INSERT, sequences and references unchanged
- **Trigger & FK safe** — uses `session_replication_role = replica` on its own session only; other sessions are unaffected

### Debloat Modes

| Mode | Workers | Passes | Efficiency | Use Case |
|------|---------|--------|------------|----------|
| **default** | 2 | 2 | ~99% | Balanced for most workloads |
| **fast** | 4 | 1 | ~97% | When speed matters more than perfection |
| **slow** | 1 | 3 | ~99-100% | Minimal impact on production (with `--delay`) |

## Output Examples

### Text Output (--estimate)

```
qwash – 3 tables analyzed

SUMMARY

  Tables analyzed           : 3
  Tables with bloat         : 2 (66.7%)

  Total database size       : 1.9 GB
  Total bloat detected      : 1.1 GB (57.9%)
  Reclaimable space         : 1.1 GB

CRITICAL BLOAT (≥ 50%)

  Table                                            Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.orders                                   1.2 GB     892.0 MB     74.33%
  public.order_items                            567.0 MB     234.0 MB     41.27%

  Total: 2 tables | 1.1 GB bloat reclaimable
```

### Text Output (--estimate --toast)

```
qwash – 3 tables with TOAST analyzed

TOAST BLOAT SUMMARY

  Tables analyzed           : 3
  Tables with bloat         : 1 (33.3%)

  Total TOAST size          : 52.0 MB
  Total bloat detected      : 27.4 MB (52.7%)
  Reclaimable space         : 27.4 MB

CRITICAL BLOAT (≥ 50%)

  Table                                      TOAST Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.messages                               39.1 MB      27.4 MB     70.00%

  Total: 1 tables | 27.4 MB bloat reclaimable

UNRELIABLE ESTIMATES (< 10 MB)

  Table                                      TOAST Size        Bloat    Bloat %
  ---------------------------------------------------------------------------------
  public.small_table                           800.0 KB          N/A          -
```

TOAST bloat estimation requires recent `VACUUM` (not just `ANALYZE`) for accurate `pg_class` stats. Tables with TOAST data smaller than 10 MB are flagged as unreliable.

### Text Output (--estimate -t table)

```
BLOAT ESTIMATION

public.orders

  Size        : 1.2 GB
  Bloat       : 892.0 MB
  Bloat %     : 74.33%
  Pages       : 157286
  Min pages   : 40384
  Live tuples : 1250000
  Dead tuples : 3750000
  Fill factor : 100
```

### JSON Output (--estimate --json)

```json
{
  "tables": [
    {
      "schema": "public",
      "table_name": "orders",
      "table_size": 1288490188,
      "bloat_size": 935329792,
      "bloat_ratio": 74.3,
      "pages": 157286,
      "min_pages": 40384,
      "live_tuples": 1250000,
      "dead_tuples": 3750000,
      "fill_factor": 100
    }
  ],
  "indexes": null
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
