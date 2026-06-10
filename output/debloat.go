package output

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"qwash/analysis"
	"sort"
	"time"
)

// DebloatOptions contains options for formatting debloat output
type DebloatOptions struct {
	FastMode          bool
	SlowMode          bool
	DryRun            bool
	LimitReached      bool
	InitialBloat      int64
	TotalDatabaseSize int64 // Total size of all processed tables (for remaining % calculation)
	Workers           int   // Number of parallel workers (1 = sequential)
}

// PrintDebloatSummary prints a text summary of debloat results
func PrintDebloatSummary(results []analysis.DebloatResult, totalDuration time.Duration, opts DebloatOptions) {
	var totalPagesRemoved int
	var totalBytesRemoved int64
	var tablesCompacted int
	var errors int
	var errorList []analysis.DebloatResult

	for _, r := range results {
		if r.Error != "" {
			errors++
			errorList = append(errorList, r)
		} else if r.BloatRemoved > 0 {
			totalPagesRemoved += r.BloatRemoved
			totalBytesRemoved += int64(r.BloatRemoved) * 8192
			tablesCompacted++
		}
	}

	// Header
	tableWord := "tables"
	if len(results) == 1 {
		tableWord = "table"
	}
	if len(results) > 0 && results[0].DryRun {
		fmt.Printf("qwash – dry-run on %d %s\n\n", len(results), tableWord)
	} else {
		fmt.Printf("qwash – %d %s processed\n\n", len(results), tableWord)
	}

	// Summary section
	fmt.Println(bold("SUMMARY"))
	fmt.Println()
	fmt.Printf("  Tables processed          : %d\n", len(results))

	// Mode
	modeName := "default"
	if opts.FastMode {
		modeName = "fast"
	} else if opts.SlowMode {
		modeName = "slow"
	}
	if opts.Workers > 1 {
		modeName += fmt.Sprintf(" (%d workers)", opts.Workers)
	}
	if opts.DryRun {
		modeName += " dry-run"
	}
	fmt.Printf("  Mode                      : %s\n", modeName)

	if errors > 0 {
		fmt.Printf("  Errors                    : %d\n", errors)
	}

	// Bloat removed with percentage of final database size remaining
	if opts.InitialBloat > 0 && opts.TotalDatabaseSize > 0 {
		remainingBloat := opts.InitialBloat - totalBytesRemoved
		if remainingBloat < 0 {
			remainingBloat = 0
		}
		// Calculate remaining bloat as percentage of FINAL database size (after compaction)
		// This matches what --estimate would show after running debloat
		finalDatabaseSize := opts.TotalDatabaseSize - totalBytesRemoved
		if finalDatabaseSize <= 0 {
			finalDatabaseSize = opts.TotalDatabaseSize
		}
		remainingPct := float64(remainingBloat) * 100.0 / float64(finalDatabaseSize)
		if remainingPct < 0.1 {
			fmt.Printf("  Bloat removed             : %s (< 0.1%% remaining)\n", FormatSize(totalBytesRemoved))
		} else {
			fmt.Printf("  Bloat removed             : %s (%.1f%% remaining)\n", FormatSize(totalBytesRemoved), remainingPct)
		}
	} else {
		fmt.Printf("  Bloat removed             : %s\n", FormatSize(totalBytesRemoved))
	}
	fmt.Printf("  Duration                  : %s\n", totalDuration.Round(time.Millisecond))
	if opts.LimitReached {
		fmt.Printf("  Status                    : limit reached\n")
	}
	fmt.Println()

	// Errors section (if any)
	if len(errorList) > 0 {
		fmt.Println(bold("ERRORS"))
		fmt.Println()
		for _, r := range errorList {
			fmt.Printf("  %-40s %s\n", r.Table, r.Error)
		}
		fmt.Println()
	}

	// Detail section: per-table breakdown sorted by bloat removed (descending)
	if tablesCompacted > 0 {
		var compacted []analysis.DebloatResult
		for _, r := range results {
			if r.BloatRemoved > 0 {
				compacted = append(compacted, r)
			}
		}

		sort.Slice(compacted, func(i, j int) bool {
			return compacted[i].BloatRemoved > compacted[j].BloatRemoved
		})

		fmt.Println(bold("DETAIL"))
		fmt.Println()
		fmt.Printf("  %-40s %12s %12s %12s\n", "Table", "Before", "After", "Removed")
		fmt.Println("  " + repeatString("-", 81))
		for _, r := range compacted {
			fmt.Printf("  %-40s %12s %12s %12s\n",
				truncateString(r.Table, 40),
				FormatSize(int64(r.InitialPages)*8192),
				FormatSize(int64(r.FinalPages)*8192),
				FormatSize(int64(r.BloatRemoved)*8192),
			)
		}
		fmt.Println()
	}
}

// PrintDebloatJSON prints debloat results in JSON format
func PrintDebloatJSON(results []analysis.DebloatResult, totalDuration time.Duration, opts DebloatOptions) {
	// Determine mode
	mode := "default"
	if opts.FastMode {
		mode = "fast"
	} else if opts.SlowMode {
		mode = "slow"
	}
	if opts.DryRun {
		mode += " dry-run"
	}

	// Calculate totals
	var totalPagesRemoved int
	var totalBytesRemoved int64
	var tablesCompacted int
	var errors int

	for _, r := range results {
		if r.Error != "" {
			errors++
		} else if r.BloatRemoved > 0 {
			totalPagesRemoved += r.BloatRemoved
			totalBytesRemoved += r.BloatRemovedBytes
			tablesCompacted++
		}
	}

	data := struct {
		Summary struct {
			TablesProcessed   int    `json:"tables_processed"`
			TablesCompacted   int    `json:"tables_compacted"`
			Errors            int    `json:"errors,omitempty"`
			Mode              string `json:"mode"`
			Workers           int    `json:"workers,omitempty"`
			TotalPagesRemoved int    `json:"total_pages_removed"`
			TotalBytesRemoved int64  `json:"total_bytes_removed"`
			DurationMs        int64  `json:"duration_ms"`
			LimitReached      bool   `json:"limit_reached,omitempty"`
		} `json:"summary"`
		Results []analysis.DebloatResult `json:"results"`
	}{}

	data.Summary.TablesProcessed = len(results)
	data.Summary.TablesCompacted = tablesCompacted
	data.Summary.Errors = errors
	data.Summary.Mode = mode
	if opts.Workers > 1 {
		data.Summary.Workers = opts.Workers
	}
	data.Summary.TotalPagesRemoved = totalPagesRemoved
	data.Summary.TotalBytesRemoved = totalBytesRemoved
	data.Summary.DurationMs = totalDuration.Milliseconds()
	data.Summary.LimitReached = opts.LimitReached
	data.Results = results

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		slog.Error("failed to marshal JSON", "error", err)
		os.Exit(1)
	}
	fmt.Println(string(jsonBytes))
}

// truncateString truncates a string to maxLen, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
