# SQL Scripts

This folder contains all SQL queries used by qwash for bloat estimation and reduction.

## Contents

### Bloat Estimation Queries

- **`table_bloat.sql`**: Estimate table bloat using the ioguix method
  - Calculates bloat percentage and size for all tables
  - No extensions required (pure SQL)
  - Output: table_name, live_tup, dead_tup, min_pages, actual_pages, bloat_size, bloat_pct

- **`btree_bloat.sql`**: Estimate B-Tree index bloat (work in progress)

### Algorithm Demo

- **`demo.sql`**: Step-by-step demonstration of qwash's debloat algorithm
  - Executable examples you can run in psql
  - Three demos with increasing complexity
  - Shows how rows are moved from high pages to low pages
  - Illustrates bloat reduction at each iteration

## Usage

### For Analysis (read-only)

```bash
# Estimate table bloat
psql -d mydb -f sql/table_bloat.sql
```

### For Understanding the Algorithm

Run `demo.sql` step-by-step in psql to see how qwash works:
```bash
psql -d mydb
# Then execute queries one by one from demo.sql
```

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

- **Estimation queries** (`table_bloat.sql`): 100% safe, read-only
- **Demo queries** (`demo.sql`): Creates/drops a `demo_qwash` table for demonstration
