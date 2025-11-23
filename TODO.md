# TODO

## Backend
- Implement connection handling (pgx).
- Estimate bloat for tables and indexes.
- Optimize queries for performance.
- Add dry-run mode for safe execution.
- Improve logging and error handling.

## Report
- Provide a **summary report** of estimated bloat.
- Add detailed per-table and per-index breakdown.
- Suggest **Autovacuum settings adjustments**.
- Support JSON export.

## Debloat
- Implement **non-blocking cleanup** for tables and indexes.
- Prioritize cleanup based on impact.
- Ensure transaction safety (rollback if needed).
- Implement verbosity levels (`--verbose`, `--quiet`).

## Housekeeping
- Refine CLI options (`--estimate`, `--clean`, `--summary`).
- Write initial test coverage (`go test`).
- Improve documentation (`README`, `godoc`).

## Future
- Investigate parallel processing.
- Explore TUI or web interface.
- Consider integration with monitoring tools (API).
