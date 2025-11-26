# qwash

**qwash** is a PostgreSQL bloat analysis and reduction tool. It identifies and reduces table bloat **without blocking writes** (unlike `VACUUM FULL`), making it safe for production use.

## Features

- **Bloat Estimation** — Analyze tables to identify bloat using PostgreSQL system catalogs
- **Non-blocking Bloat Reduction** — Reclaim space incrementally without exclusive locks
- **Multiple Modes** — Default, fast (~97% efficiency), or slow (minimal impact)
- **Dry-run Support** — Preview changes before applying them
- **JSON Output** — Machine-readable output for automation and monitoring
- **Limit Control** — Stop after reducing a specific amount of bloat

## Installation

```sh
git clone https://github.com/dalibo/qwash.git
cd qwash
go build -o bin/qwash
```

### Requirements

- Go 1.21+
- PostgreSQL 9.6+

## Quick Start

### Estimate Bloat

```sh
# Analyze all tables in a database
./bin/qwash --estimate -d mydb -U postgres -H localhost

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

# Fast mode (~97% efficiency, significantly faster)
./bin/qwash --debloat -d mydb -t mytable --fast

# Slow mode (minimal impact, like pg_compacttable)
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
  -D, --detail            Show detailed analysis per table and index
  -t, --table strings     Target specific table(s)
  -n, --schema strings    Target specific schema(s)
  -X, --exclude-table     Exclude specific tables
  -S, --system            Include system tables (pg_catalog, information_schema)

Debloat:
  -B, --debloat           Perform bloat reduction
      --fast              Fast mode: ~97% efficiency, significantly faster
      --slow              Slow mode: 1 page at a time with delay
      --delay int         Delay in ms between operations in slow mode (default: 10)
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

qwash analyzes PostgreSQL system catalogs (`pg_class`, `pg_stat_user_tables`, `pg_stats`) to estimate bloat without requiring the `pgstattuple` extension. It compares:

- **Actual table size** (pages currently allocated)
- **Minimum required pages** (calculated from live tuple count and average tuple size)

The difference is the estimated bloat.

### Bloat Reduction Algorithm

The debloat algorithm works by iteratively moving tuples from the end of the table to fill gaps left by deleted rows:

1. Select rows from the last N pages of the table
2. Delete and reinsert them (PostgreSQL places them in earlier free space)
3. Run `VACUUM` to release the now-empty pages at the end
4. Repeat until bloat is minimized

This approach:
- **Never blocks writes** — uses regular DML operations
- **Is transaction-safe** — can be interrupted safely
- **Works incrementally** — progress is preserved between runs

### Debloat Modes

| Mode | Efficiency | Speed | Use Case |
|------|------------|-------|----------|
| **default** | ~99-100% | Medium | Balanced for most workloads |
| **fast** | ~97% | Fast | When speed matters more than perfection |
| **slow** | ~99-100% | Slow | Minimal impact on production, with configurable delay |

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

[INFO] Use --debloat to reduce bloat (modes: --fast, default, --slow)
```

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

| Tool | Blocking | Extension | Extra Space | Incremental | Index Rebuild |
|------|----------|-----------|-------------|-------------|---------------|
| `VACUUM FULL` | Yes (exclusive lock) | No | 1x table size | No | Automatic |
| `pg_repack` | No | Yes (pg_repack) | 1x table size | No | Automatic |
| `pgcompacttable` | No | Yes (pgstattuple) | Minimal | Yes | Manual |
| **qwash** | No | No | Minimal | Yes | Optional (`--reindex`) |

## References

- [`sql/POC.sql`](sql/POC.sql) — Step-by-step SQL demo of the qwash algorithm
- [ioguix/pgsql-bloat-estimation](https://github.com/ioguix/pgsql-bloat-estimation) — Approach for stats-based bloat estimation without pgstattuple
- [dataegret/pgcompacttable](https://github.com/dataegret/pgcompacttable) — Perl tool for reorganizing bloated tables without locks

## License

PostgreSQL License. See [LICENSE.md](LICENSE.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
