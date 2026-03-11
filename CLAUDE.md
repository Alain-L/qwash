# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**qwash** is a PostgreSQL bloat analysis and reduction tool written in Go. It identifies and reduces bloat in PostgreSQL databases without blocking writes (as an alternative to `VACUUM FULL`). The tool provides detailed bloat estimation for tables, indexes, and TOAST data, and offers safe cleanup strategies to reclaim disk space.

## Build and Development Commands

### Build
```sh
go build -o bin/qwash
```

### Run Tests
```sh
go test ./...
```

### Test Database Connection
```sh
./bin/qwash --test-connection -d postgres -U postgres -H localhost -P 5432
```

### Example Usage
```sh
# Estimate bloat
./bin/qwash --estimate --dbname mydb

# Reduce bloat (debloat mode)
./bin/qwash --debloat --dbname mydb --table tablename
```

## Architecture

### Package Structure

- **`cmd/`**: CLI command definitions using Cobra framework
  - `root.go`: Main command setup with all flags and `executeAnalysis()` orchestration function

- **`db/`**: Database connection and query execution layer
  - `connection.go`: PostgreSQL connection handling using pgx/v5
  - `query.go`: Core database operations including:
    - `RunQwash()`: The primary bloat reduction algorithm (iterative page compaction)
    - `CompactTable()`: Wrapper for running multiple qwash iterations with VACUUM/ANALYZE
    - `GetBloatPages()`: Calculates number of bloated pages using embedded SQL

- **`analysis/`**: Bloat detection and estimation
  - `types.go`: Core data structures (`BloatTable`, `BloatIndex`, `ToastBloat`)
  - `table_bloat.go`: Table bloat detection logic
  - `index_bloat.go`: Index bloat detection logic
  - `toast_bloat.go`: TOAST bloat detection logic (uses pg_temp helper function)

- **`maintenance/`**: Bloat reduction strategies
  - `compact_table.go`: Alternative compaction approach using session_replication_role and forced tuple rewrites

- **`sql/`**: SQL queries
  - `embed.go`: Embeds SQL files using `//go:embed` directive
  - `table_bloat.sql`: ioguix-based table bloat estimation query (embedded)
  - `btree_bloat.sql`: B-Tree index bloat estimation query (embedded)
  - `toast_bloat.sql`: TOAST bloat estimation query (**standalone only**, not embedded — the Go code uses its own query with a pg_temp helper function for direct TOAST chunk scanning)

- **`output/`**: Result formatting and display
  - `text.go`: Human-readable text output
  - `json.go`: JSON export functionality

- **`testdata/`**: Test data generators and benchmarking scripts
  - `test_data.sql`: Creates test tables with varying bloat levels (low/medium/high) and sizes (10x/100x/1000x)
  - `benchmark.md`: Performance benchmarking documentation

### Key Implementation Details

#### Bloat Reduction Algorithm (RunQwash)

The core bloat reduction algorithm in `db/query.go:71` works by:

1. Selecting the top N pages (by ctid) from the end of the table
2. Creating a temporary table with those rows
3. Deleting the selected rows from the original table
4. Reinserting them from the temp table (forcing PostgreSQL to place them in lower pages)
5. Running VACUUM to reclaim freed pages
6. Iterating this process until bloat is reduced

This approach is transaction-safe and avoids table-level locks, making it suitable for production use.

#### SQL Query Embedding

SQL queries are embedded at compile time using Go's `//go:embed` directive in `sql/embed.go`. This means:
- SQL files in `sql/` are bundled into the binary
- Queries can be modified in `sql/*.sql` and require recompilation
- The embedded queries are accessible via package variables like `sql.TableBloatSQL`

#### Database Connection

The project uses **pgx/v5** (not lib/pq) for PostgreSQL connectivity. Connection parameters:
- Support for multiple databases, users, hosts, ports (slice-based flags)
- SSL mode configuration
- Connection pooling is not currently implemented (single connection per execution)

### CLI Flag System

The CLI uses Cobra with the following important flags:
- `-d, --dbname`: Target database(s)
- `-E, --estimate`: Display bloat report (analysis only)
- `-B, --debloat`: Perform bloat reduction
- `-t, --table`: Specify table(s) for debloat
- `-X, --exclude-table`: Exclude tables from analysis
- `-J, --json`: Output in JSON format
- `-v, --verbose`: Enable verbose output
- `-T, --test-connection`: Test connection and exit
- `--toast`: Estimate TOAST bloat only
- `--heap`: Estimate heap bloat only (default)

**Important**: `--estimate` and `--debloat` are mutually exclusive (enforced in `cmd/root.go:104`)

### Test Data Generation

The `testdata/test_data.sql` script generates test tables with:
- Three bloat levels: low (10% deleted), medium (30% deleted), high (70% deleted)
- Three size multipliers: 10x, 100x, 1000x (based on 1000 base rows)
- Variations with different fillfactors (default and 80)
- Autovacuum disabled for controlled bloat testing

## Code Quality Standards

This is a **high-quality learning project** following best practices:

- **Comments**: All functions and non-obvious logic must be well-documented
- **Commit messages**: Clear, concise, and descriptive (see CONTRIBUTING.md)
- **Tests**: Add tests where applicable (currently minimal test coverage)
- **Documentation**: Keep godoc comments up to date

## Current Implementation Status

**Implemented**:
- Database connection handling (pgx)
- CLI framework (Cobra)
- Bloat reduction algorithm (UPDATE-based compaction)
- Heap bloat estimation (ioguix approach)
- TOAST bloat estimation (pg_temp helper + chunk scanning)
- Standalone TOAST bloat query (sql/toast_bloat.sql)
- Text and JSON output formatters
- Integration tests (heap + TOAST)
- Parallel workers for debloat

**TODO**:
- Index bloat reduction
- TOAST bloat reduction (experimental, see feature/toast branch)
- Autovacuum tuning recommendations
- Detailed per-table/index breakdown (--detail flag)

## PostgreSQL Version Compatibility

- Target: PostgreSQL 9.6 and above
- Some SQL queries may need minor adaptations for older versions
- All SQL scripts are read-only/safe to execute
