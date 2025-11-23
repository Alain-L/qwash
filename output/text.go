package output

import (
	"fmt"
	"qwash/analysis"
)

// PrintBloatSummary displays a textual report of table and index bloat.
func PrintBloatSummary(tableBloat []analysis.BloatTable, indexBloat []analysis.BloatIndex) {
	fmt.Println("\nBLOAT ANALYSIS SUMMARY")
	fmt.Println("========================")

	// Display table bloat summary
	fmt.Println("\nTABLE BLOAT")
	fmt.Printf("  %-30s %-15s %-15s %-10s\n", "Table", "Size (MB)", "Bloat (MB)", "Bloat %")
	fmt.Println("  ------------------------------------------------------------------")

	for _, tbl := range tableBloat {
		fmt.Printf("  %-30s %-15.2f %-15.2f %-10.2f%%\n",
			fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName),
			float64(tbl.TableSize)/1024/1024,
			float64(tbl.BloatSize)/1024/1024,
			tbl.BloatRatio,
		)
	}

	// Display index bloat summary
	fmt.Println("\nINDEX BLOAT")
	fmt.Printf("  %-30s %-15s %-15s %-10s\n", "Index", "Size (MB)", "Bloat (MB)", "Bloat %")
	fmt.Println("  ------------------------------------------------------------------")

	for _, idx := range indexBloat {
		fmt.Printf("  %-30s %-15.2f %-15.2f %-10.2f%%\n",
			fmt.Sprintf("%s.%s", idx.Schema, idx.IndexName),
			float64(idx.IndexSize)/1024/1024,
			float64(idx.BloatSize)/1024/1024,
			idx.BloatRatio,
		)
	}

	fmt.Println("\n[INFO] Bloat analysis complete.")
}
