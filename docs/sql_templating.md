# SQL Templating Approaches in qwash

## Current Implementation vs Templated Approach

qwash has two approaches for managing SQL queries:

### 1. **Current Approach** (Hardcoded in Go)

**Files:** `db/query.go`

**Pros:**
- ✅ Direct and simple
- ✅ No template parsing overhead
- ✅ Type-safe with Go's fmt.Sprintf
- ✅ IDE autocomplete and syntax highlighting
- ✅ Easier to debug (stack traces show exact line)

**Cons:**
- ❌ SQL mixed with Go code
- ❌ Harder to audit SQL queries at a glance
- ❌ Requires recompilation to change queries

**Example:**
```go
query := fmt.Sprintf(`
    SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
    FROM %s
    GROUP BY page
    ORDER BY page DESC
    LIMIT %d`, tableName, pageCount)
```

---

### 2. **Templated Approach** (Embedded SQL Files)

**Files:**
- `sql/debloat_queries.sql` (templates)
- `sql/templates.go` (parser)
- `db/query_templated.go` (usage)

**Pros:**
- ✅ SQL separated from Go code
- ✅ Easier to audit and review queries
- ✅ SQL can be syntax-highlighted in editors
- ✅ Could theoretically change queries without recompilation (if not embedded)
- ✅ Better for SQL-focused code reviews

**Cons:**
- ❌ More complex setup (template parsing)
- ❌ Runtime template parsing overhead
- ❌ Harder to debug (errors point to template execution)
- ❌ More files to maintain
- ❌ Template syntax `{{.Variable}}` less familiar than Go

**Example:**
```sql
-- sql/debloat_queries.sql
SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
FROM {{.TableName}}
GROUP BY page
ORDER BY page DESC
LIMIT {{.PageCount}};
```

```go
// db/query_templated.go
queries, _ := sql.NewDebloatQueries()
var buf bytes.Buffer
queries.SelectHighestPages.Execute(&buf, map[string]interface{}{
    "TableName": tableName,
    "PageCount": pageCount,
})
rows, _ := tx.Query(ctx, buf.String())
```

---

## Performance Comparison

| Metric | Hardcoded | Templated |
|--------|-----------|-----------|
| Startup time | Instant | +5-10ms (template parsing) |
| Query execution | Same | Same |
| Memory | Lower | Higher (template cache) |
| Binary size | Same (both embed SQL) | Same |

---

## Recommendation

**For qwash, the hardcoded approach is recommended** for these reasons:

1. **Performance-critical**: Debloat operations run in tight loops; even microseconds matter
2. **Simple codebase**: Easier onboarding for contributors
3. **Better tooling**: Go's fmt.Sprintf has better IDE support than text/template
4. **Security**: SQL injection less likely with Go's type system vs string templates
5. **Debugging**: Easier to trace issues

**However**, the templated approach has value as:
- 📚 **Documentation** (see `sql/debloat_queries.sql`)
- 🔍 **Transparency** (users can read raw SQL)
- ✅ **Audit trail** (security reviews easier)

---

## Current Status

- **Active implementation**: Templated (`db/query.go`)
- **Documentation**: Both approaches documented
  - `sql/debloat_algorithm.sql` - Commented queries for understanding
  - `sql/debloat_queries.sql` - Template-ready queries (actively used)
- **Benchmark comparison**: Available in `db/query_bench_test.go`

**Decision rationale:**
The templated approach was chosen despite minor overhead (2.7x slower at 2092ns vs 763ns) because:
1. Overhead is negligible in practice (0.78ms on 30s operation = 0.003%)
2. SQL transparency and auditability are critical for this tool
3. All SQL is now in one place for security review
4. Template parsing happens once per operation, not per database query

---

## Hybrid Approach (Future)

A potential middle ground:

```go
// Embed queries as constants for documentation
//go:embed debloat_queries.sql
var DebloatQueriesDoc string

// But use hardcoded Go for execution (current approach)
func (db *DB) RunQwash(...) {
    // Fast, type-safe execution
    query := fmt.Sprintf(`SELECT ... FROM %s LIMIT %d`, table, count)
}
```

**Benefits:**
- ✅ SQL documented in separate file
- ✅ Fast execution with Go's fmt
- ✅ Single source of truth (SQL file = documentation)

This gives transparency without sacrificing performance.
