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
		if tbl.BloatRatio >= 10.0 {
			tablesWithBloat++
		}
	}

	totalBloatPercent := 0.0
	if totalDBSize > 0 {
		totalBloatPercent = float64(totalBloatSize) * 100.0 / float64(totalDBSize)
	}

	// Print summary header
	fmt.Printf("qwash – %d tables analyzed\n\n", len(tableBloat))
	fmt.Println(bold("SUMMARY"))
	fmt.Println()
	fmt.Printf("  Tables analyzed           : %d\n", len(tableBloat))
	fmt.Printf("  Tables with bloat         : %d (%.1f%%)\n", tablesWithBloat, float64(tablesWithBloat)*100.0/float64(len(tableBloat)))
	fmt.Println()
	fmt.Printf("  Total database size       : %s\n", FormatSize(totalDBSize))
	fmt.Printf("  Total bloat detected      : %s (%.1f%%)\n", FormatSize(totalBloatSize), totalBloatPercent)
	fmt.Printf("  Reclaimable space         : %s\n", FormatSize(totalBloatSize))
	fmt.Println()

	// Group tables by bloat severity
	var critical, high, medium []analysis.BloatTable
	for _, tbl := range tableBloat {
		if tbl.BloatRatio >= 50.0 {
			critical = append(critical, tbl)
		} else if tbl.BloatRatio >= 30.0 {
			high = append(high, tbl)
		} else if tbl.BloatRatio >= 10.0 {
			medium = append(medium, tbl)
		}
	}

	// Sort each group by bloat percentage (descending)
	sort.Slice(critical, func(i, j int) bool { return critical[i].BloatRatio > critical[j].BloatRatio })
	sort.Slice(high, func(i, j int) bool { return high[i].BloatRatio > high[j].BloatRatio })
	sort.Slice(medium, func(i, j int) bool { return medium[i].BloatRatio > medium[j].BloatRatio })

	// Print CRITICAL bloat section
	if len(critical) > 0 {
		fmt.Println(bold("CRITICAL BLOAT") + " (≥ 50%)")
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
				FormatSize(tbl.TableSize),
				FormatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
			criticalBloat += tbl.BloatSize
		}
		fmt.Println()
		fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(critical), FormatSize(criticalBloat))
		fmt.Println()
	}

	// Print HIGH bloat section
	if len(high) > 0 {
		fmt.Println(bold("HIGH BLOAT") + " (30-50%)")
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
				FormatSize(tbl.TableSize),
				FormatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
			highBloat += tbl.BloatSize
		}
		fmt.Println()
		fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(high), FormatSize(highBloat))
		fmt.Println()
	}

	// Print MEDIUM bloat section
	if len(medium) > 0 {
		fmt.Println(bold("MEDIUM BLOAT") + " (10-30%)")
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
				FormatSize(tbl.TableSize),
				FormatSize(tbl.BloatSize),
				tbl.BloatRatio,
			)
			mediumBloat += tbl.BloatSize
		}
		fmt.Println()
		fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(medium), FormatSize(mediumBloat))
		fmt.Println()
	}

	// No bloat message
	if len(critical) == 0 && len(high) == 0 && len(medium) == 0 {
		fmt.Println(bold("NO SIGNIFICANT BLOAT DETECTED"))
		fmt.Println()
		fmt.Println("  All tables have bloat < 10%")
		fmt.Println()
	}

	// Display index bloat summary (if available)
	if len(indexBloat) > 0 {
		fmt.Println(bold("INDEX BLOAT"))
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
				FormatSize(idx.IndexSize),
				FormatSize(idx.BloatSize),
				idx.BloatRatio,
			)
		}
		fmt.Println()
	}

}

// FormatSize converts bytes to human-readable format
func FormatSize(bytes int64) string {
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

// bold wraps text with ANSI bold escape codes
func bold(s string) string {
	return "\033[1m" + s + "\033[0m"
}

// PrintDetailedBloat displays detailed bloat information for specific tables
// Used when -t is combined with --estimate
func PrintDetailedBloat(tables []analysis.BloatTable) {
	fmt.Println()
	fmt.Println(bold("BLOAT ESTIMATION"))
	fmt.Println()

	for i, tbl := range tables {
		tableName := fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName)
		fmt.Println(tableName)
		fmt.Println()
		fmt.Printf("  Size        : %s\n", FormatSize(tbl.TableSize))
		fmt.Printf("  Bloat       : %s\n", FormatSize(tbl.BloatSize))
		fmt.Printf("  Bloat %%     : %.2f%%\n", tbl.BloatRatio)
		fmt.Printf("  Pages       : %d\n", tbl.Pages)
		fmt.Printf("  Min pages   : %d\n", tbl.MinPages)
		fmt.Printf("  Live tuples : %d\n", tbl.LiveTuples)
		fmt.Printf("  Dead tuples : %d\n", tbl.DeadTuples)
		fmt.Printf("  Fill factor : %d\n", tbl.FillFactor)
		fmt.Println()

		// Separator between tables (except for the last one)
		if i < len(tables)-1 {
			fmt.Println("  ---")
			fmt.Println()
		}
	}
}
