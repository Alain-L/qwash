# SQL Scripts

This folder contains all SQL queries used by qwash for bloat estimation and reduction.

## Contents

### Bloat Estimation Queries

- **`table_bloat.sql`**: Estimate table bloat using the ioguix method
  - Calculates bloat percentage and size for all tables
  - No extensions required (pure SQL)
  - Output: table_name, live_tup, dead_tup, min_pages, actual_pages, bloat_size, bloat_pct

- **`btree_bloat.sql`**: Estimate B-Tree index bloat
  - Identifies bloated indexes
  - Useful for index maintenance planning

### Debloat Algorithm Queries

- **`debloat_algorithm.sql`**: Complete documentation of qwash's debloat algorithm
  - Step-by-step SQL queries used during debloat operations
  - Includes explanatory comments for each step
  - Shows how rows are moved from high pages to low pages
  - Documents VACUUM/ANALYZE frequency strategy

## Usage

### For Analysis (read-only)

```bash
# Estimate table bloat
psql -d mydb -f sql/table_bloat.sql

# Estimate index bloat
psql -d mydb -f sql/btree_bloat.sql
```

### For Understanding the Algorithm

The `debloat_algorithm.sql` file is **documentation only** and should not be executed directly.
It shows the SQL queries used internally by qwash's `--debloat` mode.

To actually debloat tables, use the qwash CLI:
```bash
./bin/qwash --debloat -d mydb -t tablename
```

### In GUI Tools

All estimation queries work in:
- psql
- DBeaver
- pgAdmin
- Any PostgreSQL client

### Programmatic Usage

Queries are embedded in qwash using Go's `//go:embed` directive:
```go
import "qwash/sql"

// Use embedded query
rows := db.Query(sql.TableBloatSQL)
```

## Compatibility

- **PostgreSQL 9.6+**: Full compatibility
- **PostgreSQL 9.5 and below**: Minor adaptations may be needed

## Safety

- **Estimation queries** (`table_bloat.sql`, `btree_bloat.sql`): 100% safe, read-only
- **Debloat queries** (in `debloat_algorithm.sql`): Documentation only, use via CLI for safety
