# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Fixed
- **`--slow --delay` now actually throttles**: the delay was silently ignored, so slow mode ran at full speed while promising minimal production impact
- **Compaction procedure max-loops bug**: a PL/pgSQL variable scoping issue made the procedure return NULL instead of -2 when a page needed the maximum number of update rounds, aborting the whole table compaction
- **Bloat estimation filter hardening**: the per-table bloat query now fails fast if the embedded SQL marker is missing (previously it silently ran unfiltered and could target the wrong table) and passes schema/table names as query parameters
- Conflicting-lock check no longer fails spuriously when the lock holder has no `pg_stat_activity` entry (e.g. prepared transactions)
- `RESET lock_timeout` instead of `SET lock_timeout = 0`, preserving any server-side configuration
- pgx error comparisons use `errors.Is` instead of error string matching

### Changed
- **BREAKING — standard PostgreSQL client conventions**: `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE` and `~/.pgpass` are now honored; defaults follow libpq (local socket, OS user, `sslmode=prefer`) instead of the hardcoded `postgres@localhost:5432` with `sslmode=disable`
- **BREAKING — `-h`/`-p` short flags** for host/port (formerly `-H`/`-P`); help is `--help` only, like psql
- **BREAKING — `-W`/`--password` prompts** for the password interactively (psql semantics) instead of taking it as a command-line argument visible in `ps`
- **BREAKING — connection flags are single-valued**: `-d`, `-U`, `-h`, `-p` no longer accept repeated values (they silently used only the first one)
- **BREAKING — `--debloat` requires `-t` or the new `--all` flag**: a bare `qwash --debloat` no longer silently debloats the entire database
- **`--debloat --toast`/`--btree` is rejected**: only heap debloat is implemented (previously the heap was silently debloated instead)
- The compaction procedure is created in `pg_temp`: no orphan function left in `public` after an interrupted run, and no CREATE privilege needed
- `--test-connection` prints the resolved target (`user@host:port/dbname`)

### Added
- `--all` flag to explicitly debloat every table in the database
- First unit tests (`db` package: DSN building and quoting) and new integration regression tests (delay throttling, `PG*` environment support, `--all` behavior)
- CI now also runs on pushes to `dev` and `hardening`
- README: Operational Caveats section (privileges and table ownership, locks taken, `ENABLE ALWAYS`/`ENABLE REPLICA` triggers, WAL volume and logical replication, connection poolers, statistics freshness)

## [0.4.0] - 2026-04-26

### Added
- **B-Tree index bloat estimation** (`--btree`): Analyze B-Tree index bloat using `pg_stats` (`avg_width`, `null_frac`) and B-Tree page overhead, without requiring `pgstattuple`
  - Precision within ~5 pts of `pgstatindex` on bloated indexes (validated on PostgreSQL 18)
  - Severity grouping (CRITICAL ≥ 50%, HIGH 30-50%, MEDIUM 10-30%) consistent with `--heap` and `--toast`
  - Detailed per-index view with `-D` (pages, min pages, bloat pages, fillfactor)
  - JSON output via `--btree --json` (top-level `indexes` key)
  - Indexes with `name`-typed key columns flagged as unreliable (`is_na = true`)
- **Standalone B-Tree query** (`sql/btree_bloat.sql`): DBA-ready query with `pg_size_pretty` formatting, runnable without installing qwash
- **B-Tree integration tests**: 7 test cases (basic, high bloat, no bloat, IsNA, fillfactor, CLI, JSON)

## [0.3.0] - 2026-03-11

### Added
- **TOAST bloat estimation** (`--toast`): Analyze TOAST table bloat using system catalogs
  - Precision within ~0.2% of pgstattuple for tables >= 10 MB
  - Handles both EXTERNAL and EXTENDED (compressed) storage
  - Unreliable estimates (< 10 MB) flagged with warning
  - Stale stats detection (no VACUUM in 24h)
- **Standalone TOAST query** (`sql/toast_bloat.sql`): DO block + cursor approach for DBA use without installing qwash
- **`--heap` flag**: Explicitly select heap-only estimation (default when no flag specified)
- **TOAST JSON output**: `--toast --json` includes TOAST bloat in JSON report
- **TOAST integration tests**: 8 test cases covering all chunk profiles

## [0.2.0] - 2025-12-03

### Added
- **UPDATE-based compaction**: New compaction algorithm using `UPDATE SET col = col` via PL/pgSQL stored procedure
  - Inspired by pgcompacttable but trigger & FK safe using `session_replication_role = replica`
  - No DELETE/INSERT, preserves row identity and sequences
- **Safety checks**: Lock timeout and long transaction warnings before compaction
- **Parallel workers**: Process multiple tables concurrently with LPT scheduling
  - Default: 2 workers, Fast: 4 workers, Slow: 1 worker
- **Multiple passes**: Configurable passes per table (1 for fast, 2 for default, 3 for slow)
- **`--version` flag**: Display version, commit, and build date
- **CI/CD pipeline**: GitHub Actions for testing and GoReleaser for releases

### Changed
- **Compaction method**: Switched from DELETE/INSERT to UPDATE-based approach
- **Mode configuration**: `--fast` (4 workers, 1 pass), `--slow` (1 worker, 3 passes, with delay)
- **Bloat estimation**: Uses ioguix approach, reworked to run standalone without temporary tables

### Removed
- Legacy DELETE/INSERT compaction code (kept locally for reference)

## [0.1.0-alpha] - 2025-02-16

### Added
- Initial release with PostgreSQL bloat analysis
- Bloat estimation using system catalogs (no pgstattuple required)
- Basic debloat functionality with DELETE/INSERT method
- CLI interface with Cobra framework
- JSON and text output formats
- `--estimate` and `--debloat` modes
- `--dry-run` for previewing changes
- `--limit` to stop after reducing X bloat
- `--reindex` to rebuild indexes after debloat
