package output

import (
	"fmt"
	"qwash/analysis"
	"sort"
)

// PrintBloatSummary displays a textual report of table and index bloat.
// Tables are grouped by bloat severity level with summary statistics at the top.
func PrintBloatSummary(tableBloat []analysis.BloatTable, indexBloat []analysis.BloatIndex) {
	// Calculate summary statistics
	var totalDBSize, totalBloatSize int64
	var tablesWithBloat int
	for _, tbl := range tableBloat {
		totalDBSize += tbl.TableSize
		totalBloatSize += tbl.BloatSize
		if tbl.BloatRatio >= 5.0 {
			tablesWithBloat++
		}
	}

	totalBloatPercent := 0.0
	if totalDBSize > 0 {
		totalBloatPercent = float64(totalBloatSize) * 100.0 / float64(totalDBSize)
	}

	// Print summary header
	fmt.Printf("qwash – %d tables analyzed\n\n", len(tableBloat))
	fmt.Println("SUMMARY")
	fmt.Println()
	fmt.Printf("  Tables analyzed           : %d\n", len(tableBloat))
	fmt.Printf("  Tables with bloat         : %d (%.1f%%)\n", tablesWithBloat, float64(tablesWithBloat)*100.0/float64(len(tableBloat)))
	fmt.Println()
	fmt.Printf("  Total database size       : %s\n", formatSize(totalDBSize))
	fmt.Printf("  Total bloat detected      : %s (%.1f%%)\n", formatSize(totalBloatSize), totalBloatPercent)
	fmt.Printf("  Reclaimable space         : %s\n", formatSize(totalBloatSize))
	fmt.Println()

	// Group tables by bloat severity
	var critical, high, medium []analysis.BloatTable
	for _, tbl := range tableBloat {
		if tbl.BloatRatio >= 50.0 {
			critical = append(critical, tbl)
		} else if tbl.BloatRatio >= 20.0 {
			high = append(high, tbl)
		} else if tbl.BloatRatio >= 5.0 {
			medium = append(medium, tbl)
		}
	}

	// Sort each group by bloat percentage (descending)
	sort.Slice(critical, func(i, j int) bool { return critical[i].BloatRatio > critical[j].BloatRatio })
	sort.Slice(high, func(i, j int) bool { return high[i].BloatRatio > high[j].BloatRatio })
	sort.Slice(medium, func(i, j int) bool { return medium[i].BloatRatio > medium[j].BloatRatio })

	// Print CRITICAL bloat section
	if len(critical) > 0 {
		fmt.Println("CRITICAL BLOAT (≥ 50%)")
		fmt.Println()
		fmt.Printf("  %-40s %12s %12s %10s\n", "Table", "Size", "Bloat", "Bloat %")
		fmt.Println("  " + repeatString("-", 81))

		var criticalBloat int64
		for _, tbl := range critical {
			tableName := fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName)
			if len(tableName) > 40 {
				tableName = tableName[:37] + "..."
			}
			fmt.Printf("  %-40s %12s %12s %9.2f%%\n",
				tableName,
				formatSize(tbl.TableSize),
				formatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
			criticalBloat += tbl.BloatSize
		}
		fmt.Println()
		fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(critical), formatSize(criticalBloat))
		fmt.Println()
	}

	// Print HIGH bloat section
	if len(high) > 0 {
		fmt.Println("HIGH BLOAT (20-50%)")
		fmt.Println()
		fmt.Printf("  %-40s %12s %12s %10s\n", "Table", "Size", "Bloat", "Bloat %")
		fmt.Println("  " + repeatString("-", 81))

		var highBloat int64
		for _, tbl := range high {
			tableName := fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName)
			if len(tableName) > 40 {
				tableName = tableName[:37] + "..."
			}
			fmt.Printf("  %-40s %12s %12s %9.2f%%\n",
				tableName,
				formatSize(tbl.TableSize),
				formatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
			highBloat += tbl.BloatSize
		}
		fmt.Println()
		fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(high), formatSize(highBloat))
		fmt.Println()
	}

	// Print MEDIUM bloat section
	if len(medium) > 0 {
		fmt.Println("MEDIUM BLOAT (5-20%)")
		fmt.Println()
		fmt.Printf("  %-40s %12s %12s %10s\n", "Table", "Size", "Bloat", "Bloat %")
		fmt.Println("  " + repeatString("-", 81))

		var mediumBloat int64
		for _, tbl := range medium {
			tableName := fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName)
			if len(tableName) > 40 {
				tableName = tableName[:37] + "..."
			}
			fmt.Printf("  %-40s %12s %12s %9.2f%%\n",
				tableName,
				formatSize(tbl.TableSize),
				formatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
			mediumBloat += tbl.BloatSize
		}
		fmt.Println()
		fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(medium), formatSize(mediumBloat))
		fmt.Println()
	}

	// No bloat message
	if len(critical) == 0 && len(high) == 0 && len(medium) == 0 {
		fmt.Println("NO SIGNIFICANT BLOAT DETECTED")
		fmt.Println()
		fmt.Println("  All tables have bloat < 5%")
		fmt.Println()
	}

	// Display index bloat summary (if available)
	if len(indexBloat) > 0 {
		fmt.Println("INDEX BLOAT")
		fmt.Println()
		fmt.Printf("  %-40s %12s %12s %10s\n", "Index", "Size", "Bloat", "Bloat %")
		fmt.Println("  " + repeatString("-", 81))

		for _, idx := range indexBloat {
			indexName := fmt.Sprintf("%s.%s", idx.Schema, idx.IndexName)
			if len(indexName) > 40 {
				indexName = indexName[:37] + "..."
			}
			fmt.Printf("  %-40s %12s %12s %9.2f%%\n",
				indexName,
				formatSize(idx.IndexSize),
				formatSize(idx.BloatSize),
				idx.BloatRatio,
			)
		}
		fmt.Println()
	}

	// Helpful message
	if tablesWithBloat > 0 {
		fmt.Println("[INFO] Use --debloat to reduce bloat (modes: --fast, default, --slow)")
	}
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
