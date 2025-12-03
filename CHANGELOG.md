# Changelog

All notable changes to this project will be documented in this file.

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
