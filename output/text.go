package output

import (
	"fmt"
	"qwash/analysis"
	"sort"
)

// PrintBloatSummary displays a textual report of table and index bloat.
// Tables are sorted by bloat percentage (descending) and filtered by minimum threshold.
func PrintBloatSummary(tableBloat []analysis.BloatTable, indexBloat []analysis.BloatIndex) {
	fmt.Println("\nBLOAT ANALYSIS SUMMARY")
	fmt.Println("========================")

	// Filter tables with minimal bloat (< 5%)
	const minBloatThreshold = 5.0
	var filteredTables []analysis.BloatTable
	for _, tbl := range tableBloat {
		if tbl.BloatRatio >= minBloatThreshold {
			filteredTables = append(filteredTables, tbl)
		}
	}

	// Sort by bloat percentage (descending)
	sort.Slice(filteredTables, func(i, j int) bool {
		return filteredTables[i].BloatRatio > filteredTables[j].BloatRatio
	})

	// Display table bloat summary
	fmt.Println("\nTABLE BLOAT (showing tables with ≥ 5% bloat)")
	fmt.Printf("  %-45s %12s %12s %10s\n", "Table", "Size", "Bloat", "Bloat %")
	fmt.Println("  " + repeatString("-", 85))

	if len(filteredTables) == 0 {
		fmt.Println("  No tables with significant bloat found.")
	} else {
		for _, tbl := range filteredTables {
			tableName := fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName)
			if len(tableName) > 45 {
				tableName = tableName[:42] + "..."
			}
			fmt.Printf("  %-45s %12s %12s %9.2f%%\n",
				tableName,
				formatSize(tbl.TableSize),
				formatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
		}
		fmt.Printf("\n  Total: %d tables with bloat ≥ %.0f%%\n", len(filteredTables), minBloatThreshold)
		fmt.Printf("  (Hiding %d tables with bloat < %.0f%%)\n", len(tableBloat)-len(filteredTables), minBloatThreshold)
	}

	// Display index bloat summary (if available)
	if len(indexBloat) > 0 {
		fmt.Println("\nINDEX BLOAT")
		fmt.Printf("  %-45s %12s %12s %10s\n", "Index", "Size", "Bloat", "Bloat %")
		fmt.Println("  " + repeatString("-", 85))

		for _, idx := range indexBloat {
			indexName := fmt.Sprintf("%s.%s", idx.Schema, idx.IndexName)
			if len(indexName) > 45 {
				indexName = indexName[:42] + "..."
			}
			fmt.Printf("  %-45s %12s %12s %9.2f%%\n",
				indexName,
				formatSize(idx.IndexSize),
				formatSize(idx.BloatSize),
				idx.BloatRatio,
			)
		}
	}

	fmt.Println("\n[INFO] Bloat analysis complete.")
}

// formatSize converts bytes to human-readable format
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// repeatString repeats a string n times
func repeatString(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
