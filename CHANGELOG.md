# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [0.5.0] - 2026-06-17

> Full commit list on the GitHub release page.

### Changed (breaking)
- Connection follows libpq: `PG*` env vars and `~/.pgpass` honored; libpq defaults (was `postgres@localhost:5432`).
- `-h`/`-p` for host/port (were `-H`/`-P`); `-W` prompts (was a value); connection flags single-valued.
- `--debloat` requires `-t` or `--all`; `--debloat --toast`/`--btree` rejected.

### Added
- Exit codes (`0`/`1`/`2`) for automation.
- `--all` flag.
- Working `--system` (estimates `pg_catalog` tables).
- Preflight safety checks before compaction (ownership / `session_replication_role` / REPLICA IDENTITY; warns on `ENABLE ALWAYS` triggers and publications).
- Clean Ctrl-C cancellation.
- `--reindex` never falls back to a blocking REINDEX.
- `go install github.com/Alain-L/qwash@latest` (full module path).
- README: Operational Caveats section.
- First unit tests; CI on PostgreSQL 14-18 with the race detector.

### Fixed
- Never-analyzed, churned, and privilege-blocked tables flagged, not reported bloat-free (heap & TOAST).
- B-Tree indexes without column stats are listed (were silently dropped).
- Byte-exact sizes (no `pg_size_pretty` round-trip).
- Bloat query runs once per debloat (was per table and per pass).
- Schema-qualified targeting; `--schema`/`--exclude-table` honored in `--estimate`.
- `--slow --delay` actually throttles; `--limit` skips reported as skipped, not errors.
- PL/pgSQL scoping bug that could abort a table's compaction.

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
