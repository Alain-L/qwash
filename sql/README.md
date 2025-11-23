# SQL Scripts

This folder contains reusable SQL queries used by or alongside Quellog.

## Contents

- `table_bloat.sql`: estimate table bloat using the ioguix method.
- `btree_bloat.sql`: estimate B-Tree index bloat.

## Usage

These queries can be used:

- Directly in psql: `\i sql/table_bloat.sql`
- In GUI tools such as DBeaver or pgAdmin
- Dynamically from Go code using `os.ReadFile("sql/table_bloat.sql")` or similar.

## Compatibility

Compatible with PostgreSQL 9.6 and above.  
Some minor adaptations may be needed for older versions.

Each script is self-contained and safe to execute in a read-only context (no writes).
