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

// PrintToastBloatSummary displays TOAST bloat information with severity grouping
// Note: Bloat estimation is only reliable for TOAST >= 10 MB
func PrintToastBloatSummary(toastBloat []analysis.ToastBloat) {
	if len(toastBloat) == 0 {
		return
	}

	// Calculate summary statistics
	var totalToastSize, totalToastBloat int64
	var tablesWithBloat int
	for _, tb := range toastBloat {
		totalToastSize += tb.ToastSize
		if tb.BloatPct != nil {
			totalToastBloat += tb.BloatSize
			if *tb.BloatPct >= 10.0 {
				tablesWithBloat++
			}
		}
	}

	totalBloatPercent := 0.0
	if totalToastSize > 0 {
		totalBloatPercent = float64(totalToastBloat) * 100.0 / float64(totalToastSize)
	}

	// Print summary header
	fmt.Printf("qwash – %d tables with TOAST analyzed\n\n", len(toastBloat))
	fmt.Println(bold("TOAST BLOAT SUMMARY"))
	fmt.Println()
	fmt.Printf("  Tables analyzed           : %d\n", len(toastBloat))
	fmt.Printf("  Tables with bloat         : %d (%.1f%%)\n", tablesWithBloat, float64(tablesWithBloat)*100.0/float64(len(toastBloat)))
	fmt.Println()
	fmt.Printf("  Total TOAST size          : %s\n", FormatSize(totalToastSize))
	fmt.Printf("  Total bloat detected      : %s (%.1f%%)\n", FormatSize(totalToastBloat), totalBloatPercent)
	fmt.Printf("  Reclaimable space         : %s\n", FormatSize(totalToastBloat))
	fmt.Println()

	// Group tables by bloat severity (only tables with reliable estimates)
	var critical, high, medium, unreliable []analysis.ToastBloat
	for _, tb := range toastBloat {
		if tb.BloatPct == nil {
			unreliable = append(unreliable, tb)
		} else if *tb.BloatPct >= 50.0 {
			critical = append(critical, tb)
		} else if *tb.BloatPct >= 30.0 {
			high = append(high, tb)
		} else if *tb.BloatPct >= 10.0 {
			medium = append(medium, tb)
		}
	}

	// Sort each group by bloat percentage (descending)
	sort.Slice(critical, func(i, j int) bool { return *critical[i].BloatPct > *critical[j].BloatPct })
	sort.Slice(high, func(i, j int) bool { return *high[i].BloatPct > *high[j].BloatPct })
	sort.Slice(medium, func(i, j int) bool { return *medium[i].BloatPct > *medium[j].BloatPct })

	// Print CRITICAL bloat section
	if len(critical) > 0 {
		fmt.Println(bold("CRITICAL BLOAT") + " (≥ 50%)")
		fmt.Println()
		printToastBloatTable(critical)
	}

	// Print HIGH bloat section
	if len(high) > 0 {
		fmt.Println(bold("HIGH BLOAT") + " (30-50%)")
		fmt.Println()
		printToastBloatTable(high)
	}

	// Print MEDIUM bloat section
	if len(medium) > 0 {
		fmt.Println(bold("MEDIUM BLOAT") + " (10-30%)")
		fmt.Println()
		printToastBloatTable(medium)
	}

	// No bloat message
	if len(critical) == 0 && len(high) == 0 && len(medium) == 0 {
		fmt.Println(bold("NO SIGNIFICANT BLOAT DETECTED"))
		fmt.Println()
		fmt.Println("  All tables have TOAST bloat < 10%")
		fmt.Println()
	}

	// Print unreliable estimates at the end (but not empty TOAST tables)
	var unreliableNonEmpty []analysis.ToastBloat
	for _, tb := range unreliable {
		if tb.ToastSize > 0 {
			unreliableNonEmpty = append(unreliableNonEmpty, tb)
		}
	}
	if len(unreliableNonEmpty) > 0 {
		fmt.Println(bold("UNRELIABLE ESTIMATES") + " (< 10 MB)")
		fmt.Println()
		fmt.Printf("  %-40s %12s %12s %10s\n", "Table", "TOAST Size", "Bloat", "Bloat %")
		fmt.Println("  " + repeatString("-", 81))
		for _, tb := range unreliableNonEmpty {
			tableName := fmt.Sprintf("%s.%s", tb.Schema, tb.TableName)
			if len(tableName) > 40 {
				tableName = tableName[:37] + "..."
			}
			fmt.Printf("  %-40s %12s %12s %10s\n",
				tableName,
				FormatSize(tb.ToastSize),
				"N/A",
				"-",
			)
		}
		fmt.Println()
	}
}

// printToastBloatTable prints a table of TOAST bloat entries with totals
func printToastBloatTable(entries []analysis.ToastBloat) {
	fmt.Printf("  %-40s %12s %12s %10s\n", "Table", "TOAST Size", "Bloat", "Bloat %")
	fmt.Println("  " + repeatString("-", 81))

	var totalBloat int64
	for _, tb := range entries {
		tableName := fmt.Sprintf("%s.%s", tb.Schema, tb.TableName)
		if len(tableName) > 40 {
			tableName = tableName[:37] + "..."
		}
		fmt.Printf("  %-40s %12s %12s %9.2f%%\n",
			tableName,
			FormatSize(tb.ToastSize),
			FormatSize(tb.BloatSize),
			*tb.BloatPct,
		)
		totalBloat += tb.BloatSize
	}
	fmt.Println()
	fmt.Printf("  Total: %d tables | %s bloat reclaimable\n", len(entries), FormatSize(totalBloat))
	fmt.Println()
}

// PrintDetailedToastBloat displays detailed TOAST bloat information for specific tables
// Used when -t is combined with --estimate --toast
func PrintDetailedToastBloat(toastData []analysis.ToastBloat) {
	fmt.Println()
	fmt.Println(bold("TOAST BLOAT ESTIMATION"))
	fmt.Println()

	for i, tb := range toastData {
		tableName := fmt.Sprintf("%s.%s", tb.Schema, tb.TableName)
		fmt.Println(tableName)
		fmt.Println()
		fmt.Printf("  TOAST Size  : %s\n", FormatSize(tb.ToastSize))

		if tb.BloatPct != nil {
			fmt.Printf("  Bloat       : %s\n", FormatSize(tb.BloatSize))
			fmt.Printf("  Bloat %%     : %.2f%%\n", *tb.BloatPct)
		} else if tb.Warning == "no chunks" {
			fmt.Printf("  Bloat       : N/A (no chunks)\n")
			fmt.Printf("  Bloat %%     : -\n")
		} else {
			fmt.Printf("  Bloat       : N/A\n")
			fmt.Printf("  Bloat %%     : -\n")
		}

		fmt.Printf("  Pages       : %d\n", tb.ToastPages)
		fmt.Printf("  Chunks      : %d\n", tb.ToastChunks)
		fmt.Println()

		// Separator between tables (except for the last one)
		if i < len(toastData)-1 {
			fmt.Println("  ---")
			fmt.Println()
		}
	}
}
