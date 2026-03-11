# Changelog

All notable changes to this project will be documented in this file.

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
