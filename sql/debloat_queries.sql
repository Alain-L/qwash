-- ============================================================================
-- qwash Debloat Queries - Executable Templates
-- ============================================================================
-- These queries are used by the Go code via text/template.
-- Template variables are denoted with {{.VariableName}}
-- ============================================================================

-- QUERY: select_highest_pages
-- Identifies the N highest pages in a table by CTID
-- Variables: {{.TableName}}, {{.PageCount}}
SELECT ltrim(split_part(ctid::text, ',', 1), '(')::int AS page
FROM {{.TableName}}
GROUP BY page
ORDER BY page DESC
LIMIT {{.PageCount}};

-- QUERY: create_temp_table
-- Creates temporary table with rows from specified pages
-- Variables: {{.TableName}}, {{.InClause}}
CREATE TEMPORARY TABLE qwash ON COMMIT DROP AS
SELECT * FROM {{.TableName}}
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN ({{.InClause}});

-- QUERY: delete_from_table
-- Deletes rows from specified pages
-- Variables: {{.TableName}}, {{.InClause}}
DELETE FROM {{.TableName}}
WHERE ltrim(split_part(ctid::text, ',', 1), '(')::int IN ({{.InClause}});

-- QUERY: reinsert_from_temp
-- Reinserts rows from temporary table
-- Variables: {{.TableName}}
INSERT INTO {{.TableName}}
SELECT * FROM qwash;

-- QUERY: vacuum_table
-- Runs VACUUM on table
-- Variables: {{.TableName}}
VACUUM {{.TableName}};

-- QUERY: analyze_table
-- Runs ANALYZE on table
-- Variables: {{.TableName}}
ANALYZE {{.TableName}};

-- QUERY: verify_highest_ctids
-- Shows highest CTIDs after compaction (for verification)
-- Variables: {{.TableName}}
SELECT ctid
FROM {{.TableName}}
ORDER BY ctid DESC
LIMIT 2;
