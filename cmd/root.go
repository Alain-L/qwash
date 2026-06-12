package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qwash/analysis"
	"qwash/db"
	"qwash/output"

	"github.com/spf13/cobra"
)

// Global Flags
var (
	// Database connection
	dbName       []string // --dbname
	user         []string // --dbuser
	host         []string // --host
	port         []string // --port
	pass         string   // --password
	sslMode      string   // --sslmode
	testConnFlag bool     // --test-connection

	// Targeting options
	targetTables  []string // --table (-t)
	targetSchemas []string // --schema (-n)
	excludeTbl    []string // --exclude-table
	systemFlag    bool     // --system (-S) include system tables

	// Analysis options
	estimateFlag bool // --estimate (-E)
	detailFlag   bool // --detail (-D)

	// Target selection (what to analyze/debloat)
	heapFlag  bool // --heap (default if neither specified)
	toastFlag bool // --toast
	btreeFlag bool // --btree

	// Debloat options
	debloatFlag bool   // --debloat (-B)
	allFlag     bool   // --all (debloat every table when no -t is given)
	fastFlag    bool   // --fast
	slowFlag    bool   // --slow (1 page at a time with delay)
	delayMs     int    // --delay (milliseconds between operations in slow mode)
	dryRunFlag  bool   // --dry-run
	reindexFlag bool   // --reindex
	limitStr    string // --limit (stop after reducing X bloat: 500MB, 1GB, 50%)
	jobsFlag    int    // --jobs (-j) number of parallel workers (0 = auto)

	// Output options
	verboseFlag bool // --verbose
	jsonFlag    bool // --json
)

// rootCmd is the main command for qwash
var rootCmd = &cobra.Command{
	Use:   "qwash",
	Short: "qwash is a PostgreSQL bloat analysis and reduction tool",
	Long: `qwash analyzes PostgreSQL catalogs to detect table and index bloat.
It provides estimation, reporting, and optionally helps remove unnecessary bloat without downtime.`,
	Run: executeAnalysis,
}

// Execute runs the CLI
func Execute(version, commit, date string) {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)

	initLogger()

	if err := rootCmd.Execute(); err != nil {
		fatal("command failed", "error", err)
	}
}

// init sets up the CLI flags
func init() {
	// Database connection options
	rootCmd.PersistentFlags().StringSliceVarP(&dbName, "dbname", "d", nil,
		"Target database(s) for analysis")
	rootCmd.PersistentFlags().StringSliceVarP(&user, "dbuser", "U", nil,
		"Database user(s) for connection")
	rootCmd.PersistentFlags().StringSliceVarP(&host, "host", "H", nil,
		"Database host(s) (default: localhost)")
	rootCmd.PersistentFlags().StringSliceVarP(&port, "port", "P", nil,
		"Database port(s) (default: 5432)")
	rootCmd.PersistentFlags().StringVarP(&pass, "password", "W", "",
		"Database password (optional)")
	rootCmd.PersistentFlags().StringVar(&sslMode, "sslmode", "disable",
		"SSL mode (disable, require, verify-ca, verify-full)")

	// Database testing
	rootCmd.PersistentFlags().BoolVarP(&testConnFlag, "test-connection", "T", false,
		"Test the database connection and exit")

	// Targeting options
	rootCmd.PersistentFlags().StringSliceVarP(&targetTables, "table", "t", nil,
		"Target specific table(s) (can be repeated)")
	rootCmd.PersistentFlags().StringSliceVarP(&targetSchemas, "schema", "n", nil,
		"Target specific schema(s) (can be repeated)")
	rootCmd.PersistentFlags().StringSliceVarP(&excludeTbl, "exclude-table", "X", nil,
		"Exclude specific tables from analysis")
	rootCmd.PersistentFlags().BoolVarP(&systemFlag, "system", "S", false,
		"Include system tables (pg_catalog, information_schema)")

	// Analysis options
	rootCmd.PersistentFlags().BoolVarP(&estimateFlag, "estimate", "E", false,
		"Display a report of estimated bloat")
	rootCmd.PersistentFlags().BoolVarP(&detailFlag, "detail", "D", false,
		"Show detailed bloat analysis per table and index")
	rootCmd.PersistentFlags().BoolVar(&heapFlag, "heap", false,
		"Analyze heap bloat (default if no --heap, --toast or --btree specified)")
	rootCmd.PersistentFlags().BoolVar(&toastFlag, "toast", false,
		"Analyze TOAST bloat")
	rootCmd.PersistentFlags().BoolVar(&btreeFlag, "btree", false,
		"Analyze B-Tree index bloat")

	// Debloat options
	rootCmd.PersistentFlags().BoolVarP(&debloatFlag, "debloat", "B", false,
		"Perform bloat reduction on tables")
	rootCmd.PersistentFlags().BoolVar(&allFlag, "all", false,
		"Debloat every table in the database (required when --table is not given)")
	rootCmd.PersistentFlags().BoolVar(&fastFlag, "fast", false,
		"Fast mode: 4 threads, 1 pass (default: 2 threads, 2 passes)")
	rootCmd.PersistentFlags().BoolVar(&slowFlag, "slow", false,
		"Slow mode: 1 thread, 3 passes with delay between operations")
	rootCmd.PersistentFlags().IntVar(&delayMs, "delay", 10,
		"Delay in milliseconds between page rounds in slow mode")
	rootCmd.PersistentFlags().BoolVar(&dryRunFlag, "dry-run", false,
		"Show what would be done without making changes")
	rootCmd.PersistentFlags().BoolVar(&reindexFlag, "reindex", false,
		"Rebuild indexes after debloat (REINDEX CONCURRENTLY)")
	rootCmd.PersistentFlags().StringVar(&limitStr, "limit", "",
		"Stop after reducing X bloat (e.g., 500MB, 1GB, 50%)")
	rootCmd.PersistentFlags().IntVarP(&jobsFlag, "jobs", "j", 0,
		"Number of parallel workers (default: 2, 4 with --fast, 1 with --slow)")

	// Output options
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false,
		"Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&jsonFlag, "json", "J", false,
		"Output results in JSON format")
}

// executeAnalysis is the core function that orchestrates the pipeline
func executeAnalysis(cmd *cobra.Command, args []string) {
	// Raise verbosity to INFO when requested; otherwise INFO diagnostics stay
	// quiet and only warnings/errors are shown.
	if verboseFlag {
		setLogLevel(slog.LevelInfo)
	}

	// Build connection config
	dbConfig := db.Config{
		Host:     getFirstOrDefault(host, "localhost"),
		Port:     getFirstOrDefault(port, "5432"),
		User:     getFirstOrDefault(user, "postgres"),
		Password: pass,
		Database: getFirstOrDefault(dbName, "postgres"),
		SSLMode:  sslMode,
	}

	// Step 1: Validate flag combinations
	if estimateFlag && debloatFlag {
		fatal("--estimate (-E) and --debloat (-B) cannot be used together")
	}

	// --toast and --btree are analysis-only: only heap debloat is implemented.
	// Without this guard, --debloat --toast would silently debloat the heap.
	if debloatFlag && (toastFlag || btreeFlag) {
		fatal("--toast and --btree are not supported with --debloat (-B); only heap debloat is implemented")
	}

	// Debloating the whole database must be an explicit decision: require
	// either a table list (-t) or the --all flag.
	if debloatFlag && len(targetTables) == 0 && !allFlag {
		fatal("--debloat requires --table (-t) to target specific tables, or --all to debloat every table")
	}
	if allFlag && !debloatFlag {
		fatal("--all requires --debloat (-B)")
	}
	if allFlag && len(targetTables) > 0 {
		fatal("--all and --table (-t) are mutually exclusive")
	}

	// --reindex requires --debloat
	if reindexFlag && !debloatFlag {
		fatal("--reindex requires --debloat (-B)")
	}

	// --fast, --slow, --dry-run require --debloat
	if (fastFlag || slowFlag || dryRunFlag) && !debloatFlag {
		fatal("--fast, --slow, and --dry-run require --debloat (-B)")
	}

	// --fast and --slow are mutually exclusive
	if fastFlag && slowFlag {
		fatal("--fast and --slow are mutually exclusive")
	}

	// --delay requires --slow
	if cmd.Flags().Changed("delay") && !slowFlag {
		fatal("--delay can only be used with --slow mode")
	}

	// Validate --delay value
	if delayMs < 0 {
		fatal("--delay must be >= 0", "got", delayMs)
	}
	if delayMs > 10000 {
		fatal("--delay must be <= 10000ms; use lower values for reasonable performance", "got", delayMs)
	}

	// --jobs requires --debloat
	if cmd.Flags().Changed("jobs") && !debloatFlag {
		fatal("--jobs (-j) requires --debloat (-B)")
	}

	// Validate --limit value (early validation before DB connection)
	if limitStr != "" {
		if _, _, err := parseLimit(limitStr); err != nil {
			fatal("invalid --limit value", "error", err)
		}
		// --limit requires --debloat
		if !debloatFlag {
			fatal("--limit requires --debloat (-B)")
		}
	}

	// Require at least one mode
	if !estimateFlag && !debloatFlag && !testConnFlag && !detailFlag {
		// Default to --estimate if no mode specified
		estimateFlag = true
	}

	// Default to --heap if no target specified
	if !heapFlag && !toastFlag && !btreeFlag {
		heapFlag = true
	}

	// Step 2: Test database connection if requested
	if testConnFlag {
		if verboseFlag {
			fmt.Printf("Connecting to %s@%s:%s/%s...\n",
				dbConfig.User, dbConfig.Host, dbConfig.Port, dbConfig.Database)
		}
		connection, err := db.Connect(dbConfig, verboseFlag)
		if err != nil {
			fatal("connection failed", "error", err)
		}
		defer connection.Close()

		fmt.Println("Connection OK")
		databases, err := connection.ListDatabases()
		if err == nil {
			fmt.Println("Available databases:")
			for _, db := range databases {
				fmt.Printf("  - %s\n", db)
			}
		}
		return
	}

	// Step 3: Establish database connection
	connection, err := db.Connect(dbConfig, verboseFlag)
	if err != nil {
		fatal("failed to connect", "error", err)
	}
	defer connection.Close()

	// Step 4: Determine operation mode
	switch {
	case estimateFlag:
		runEstimate(connection)
		return

	case debloatFlag:
		runDebloat(connection)
		return

	case detailFlag:
		fmt.Println("[INFO] Detailed bloat analysis per table and index not yet implemented")
		fmt.Println("       Use --estimate for bloat estimation or --debloat to reduce bloat")
		return

	default:
		fmt.Println("[INFO] Global bloat summary not yet implemented")
		fmt.Println("       Use --estimate for bloat estimation or --debloat to reduce bloat")
	}
}

// Helper function to get the first value from a slice or return a default value
func getFirstOrDefault(arr []string, defaultValue string) string {
	if len(arr) > 0 {
		return arr[0]
	}
	return defaultValue
}

// runEstimate executes the bloat estimation report
func runEstimate(connection *db.DB) {
	if verboseFlag {
		fmt.Println("Running bloat estimation...")
		if len(targetSchemas) > 0 {
			fmt.Printf("  Schemas: %v\n", targetSchemas)
		}
		if len(targetTables) > 0 {
			fmt.Printf("  Tables: %v\n", targetTables)
		}
		if systemFlag {
			fmt.Println("  Including system tables")
		}
		if heapFlag {
			fmt.Println("  Heap bloat estimation")
		}
		if toastFlag {
			fmt.Println("  TOAST bloat estimation")
		}
		if btreeFlag {
			fmt.Println("  B-Tree index bloat estimation")
		}
	}

	// Analyze table (heap) bloat if --heap is enabled
	var tableBloat []analysis.BloatTable
	var err error
	if heapFlag {
		tableBloat, err = analysis.DetectTableBloat(context.Background(), connection)
		if err != nil {
			fatal("failed to analyze table bloat", "error", err)
		}
	}

	// Analyze TOAST bloat if --toast is enabled
	var toastBloat []analysis.ToastBloat
	if toastFlag {
		toastBloat, err = analysis.DetectToastBloat(context.Background(), connection)
		if err != nil {
			slog.Warn("failed to analyze TOAST bloat", "error", err)
			// Continue without TOAST data
		}
	}

	// Analyze B-Tree index bloat if --btree is enabled
	var indexBloat []analysis.BloatIndex
	if btreeFlag {
		indexBloat, err = analysis.DetectBtreeIndexBloat(context.Background(), connection)
		if err != nil {
			slog.Warn("failed to analyze B-Tree index bloat", "error", err)
			// Continue without index data
		}
	}

	// Filter by specific tables if -t is provided
	if len(targetTables) > 0 {
		// Detailed view for specific tables
		if jsonFlag {
			var filtered []analysis.BloatTable
			if heapFlag {
				filtered = filterTablesByName(tableBloat, targetTables)
			}
			var filteredToast []analysis.ToastBloat
			if toastFlag {
				filteredToast = filterToastByName(toastBloat, targetTables)
			}
			var filteredIndex []analysis.BloatIndex
			if btreeFlag {
				filteredIndex = filterIndexByTable(indexBloat, targetTables)
			}
			output.PrintBloatJSON(filtered, filteredIndex, filteredToast)
		} else {
			// Print heap bloat if enabled
			if heapFlag {
				filtered := filterTablesByName(tableBloat, targetTables)
				if len(filtered) > 0 {
					output.PrintDetailedBloat(filtered)
				} else {
					fmt.Printf("No matching tables found for: %v\n", targetTables)
				}
			}
			// Print TOAST bloat if enabled
			if toastFlag {
				filteredToast := filterToastByName(toastBloat, targetTables)
				if len(filteredToast) > 0 {
					output.PrintDetailedToastBloat(filteredToast)
				}
			}
			// Print B-Tree index bloat if enabled
			if btreeFlag {
				filteredIndex := filterIndexByTable(indexBloat, targetTables)
				if len(filteredIndex) > 0 {
					output.PrintDetailedIndexBloat(filteredIndex)
				}
			}
		}
		return
	}

	// Display results based on output format
	if jsonFlag {
		output.PrintBloatJSON(tableBloat, indexBloat, toastBloat)
	} else {
		// Print heap bloat if enabled
		if heapFlag && len(tableBloat) > 0 {
			output.PrintBloatSummary(tableBloat, nil)
		}
		// Print B-Tree index bloat if enabled
		if btreeFlag && len(indexBloat) > 0 {
			output.PrintIndexBloatSummary(indexBloat)
		}
		// Print TOAST bloat if enabled
		if toastFlag && len(toastBloat) > 0 {
			output.PrintToastBloatSummary(toastBloat)
		}
	}
}

// filterTablesByName filters bloat tables by name (supports schema.table or just table)
func filterTablesByName(tables []analysis.BloatTable, targetNames []string) []analysis.BloatTable {
	var result []analysis.BloatTable
	for _, tbl := range tables {
		fullName := fmt.Sprintf("%s.%s", tbl.Schema, tbl.TableName)
		for _, target := range targetNames {
			// Match full name (schema.table) or just table name
			if fullName == target || tbl.TableName == target {
				result = append(result, tbl)
				break
			}
		}
	}
	return result
}

// filterToastByName filters TOAST bloat by table name (supports schema.table or just table)
func filterToastByName(toastData []analysis.ToastBloat, targetNames []string) []analysis.ToastBloat {
	var result []analysis.ToastBloat
	for _, tb := range toastData {
		fullName := fmt.Sprintf("%s.%s", tb.Schema, tb.TableName)
		for _, target := range targetNames {
			// Match full name (schema.table) or just table name
			if fullName == target || tb.TableName == target {
				result = append(result, tb)
				break
			}
		}
	}
	return result
}

// filterIndexByTable filters index bloat by parent table name (supports schema.table or just table)
func filterIndexByTable(indexes []analysis.BloatIndex, targetNames []string) []analysis.BloatIndex {
	var result []analysis.BloatIndex
	for _, idx := range indexes {
		fullName := fmt.Sprintf("%s.%s", idx.Schema, idx.TableName)
		for _, target := range targetNames {
			if fullName == target || idx.TableName == target {
				result = append(result, idx)
				break
			}
		}
	}
	return result
}

// runDebloat executes the bloat reduction process
func runDebloat(connection *db.DB) {
	// Warning for system tables
	if systemFlag {
		fmt.Println("WARNING: You are about to debloat system tables!")
		fmt.Println("This can be dangerous and may affect database stability.")
		fmt.Print("Are you sure you want to continue? [y/N]: ")

		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return
		}
	}

	// Parse limit if specified
	limitBytes, limitPercent, err := parseLimit(limitStr)
	if err != nil {
		fatal("invalid --limit value", "error", err)
	}

	// Show mode (verbose only, except dry-run which is always shown in text mode)
	if dryRunFlag && !jsonFlag {
		fmt.Println("DRY-RUN MODE: No changes will be made")
	}
	if verboseFlag {
		if fastFlag {
			fmt.Println("FAST MODE: 4 threads, 1 pass")
		}
		if limitBytes > 0 {
			fmt.Printf("LIMIT: %s\n", output.FormatSize(limitBytes))
		} else if limitPercent > 0 {
			fmt.Printf("LIMIT: %.1f%% of total bloat\n", limitPercent)
		}
	}

	// Get list of tables to process
	tables, err := getTargetTables(connection)
	if err != nil {
		fatal("failed to get target tables", "error", err)
	}

	if len(tables) == 0 {
		fmt.Println("No tables to debloat.")
		return
	}

	// Calculate total bloat, total size, and build map for LPT scheduling
	// LPT (Longest Processing Time First) sorts tables by bloat size descending
	// to optimize parallel processing - biggest tables first so smaller ones fill gaps
	var totalBloat int64
	var totalDatabaseSize int64
	tableBloatPages := make(map[string]int)
	for _, table := range tables {
		bloatPages, err := connection.GetBloatPages(table)
		if err == nil && bloatPages > 0 {
			totalBloat += int64(bloatPages) * 8192 // Convert pages to bytes (8KB per page)
			tableBloatPages[table] = bloatPages
		}
		// Get total table size for percentage calculation
		tablePages, err := connection.GetTablePages(table)
		if err == nil && tablePages > 0 {
			totalDatabaseSize += int64(tablePages) * 8192
		}
	}

	// Sort tables by bloat size (descending) for optimal parallel scheduling
	sort.Slice(tables, func(i, j int) bool {
		return tableBloatPages[tables[i]] > tableBloatPages[tables[j]]
	})

	if limitPercent > 0 && totalBloat > 0 {
		limitBytes = int64(float64(totalBloat) * limitPercent / 100.0)
		if verboseFlag {
			fmt.Printf("  Total bloat detected: %s, limit set to %s\n",
				output.FormatSize(totalBloat), output.FormatSize(limitBytes))
		}
	}

	// Determine number of workers
	numWorkers := jobsFlag
	if numWorkers <= 0 {
		// --fast=4, default=2, --slow=1
		if fastFlag {
			numWorkers = 4
		} else if slowFlag {
			numWorkers = 1
		} else {
			numWorkers = 2
		}
	}
	// Cap at number of tables
	if numWorkers > len(tables) {
		numWorkers = len(tables)
	}
	// Slow mode should use 1 worker (defeats purpose of being gentle otherwise)
	if slowFlag && numWorkers > 1 {
		if verboseFlag {
			fmt.Println("Note: --slow mode uses single worker for minimal database impact")
		}
		numWorkers = 1
	}

	if verboseFlag {
		fmt.Printf("Tables to process: %d (using %d workers)\n", len(tables), numWorkers)
		for _, t := range tables {
			fmt.Printf("  - %s\n", t)
		}
	}

	// Set delay for --slow mode (used by CompactTableUpdate)
	if slowFlag {
		connection.DelayMs = delayMs
	}

	startTime := time.Now()
	var results []analysis.DebloatResult
	var limitReached bool

	if numWorkers == 1 {
		// Sequential mode (original behavior)
		results, limitReached = runDebloatSequential(connection, tables, limitBytes)
	} else {
		// Parallel mode
		results, limitReached = runDebloatParallel(connection, tables, numWorkers, limitBytes)
	}

	totalDuration := time.Since(startTime)

	// Output results
	opts := output.DebloatOptions{
		FastMode:          fastFlag,
		SlowMode:          slowFlag,
		DryRun:            dryRunFlag,
		LimitReached:      limitReached,
		InitialBloat:      totalBloat,
		TotalDatabaseSize: totalDatabaseSize,
		Workers:           numWorkers,
	}
	if jsonFlag {
		output.PrintDebloatJSON(results, totalDuration, opts)
	} else {
		output.PrintDebloatSummary(results, totalDuration, opts)
	}
}

// runDebloatSequential processes tables one at a time (original behavior)
func runDebloatSequential(connection *db.DB, tables []string, limitBytes int64) ([]analysis.DebloatResult, bool) {
	var results []analysis.DebloatResult
	var totalBloatRemoved int64
	limitReached := false

	// Set total tables for progress display
	connection.TotalTables = len(tables)
	// Silence internal progress when showing main progress bar (non-verbose, non-json)
	connection.SilentProgress = jsonFlag || (!verboseFlag && !dryRunFlag)

	for i, table := range tables {
		// Check if limit is reached
		if limitBytes > 0 && totalBloatRemoved >= limitBytes {
			if verboseFlag {
				fmt.Printf("  %s: skipped (limit reached)\n", table)
			}
			limitReached = true
			break
		}

		// Show progress bar (same as parallel mode)
		if !jsonFlag && !verboseFlag && !dryRunFlag {
			printParallelProgress(i, len(tables), 1)
		}

		connection.CurrentTableIndex = i
		result := processTable(connection, table)
		results = append(results, result)

		if result.BloatRemoved > 0 {
			totalBloatRemoved += int64(result.BloatRemoved) * 8192
		}
	}

	// Clear progress line at the end
	if !verboseFlag && !dryRunFlag && !jsonFlag {
		fmt.Print("\r\033[K")
	}

	return results, limitReached
}

// runDebloatParallel processes tables concurrently using a worker pool
func runDebloatParallel(connection *db.DB, tables []string, numWorkers int, limitBytes int64) ([]analysis.DebloatResult, bool) {
	// Channels for work distribution and results
	tableChan := make(chan string, len(tables))
	resultChan := make(chan analysis.DebloatResult, len(tables))

	// Atomic counters for progress and limit tracking
	var tablesCompleted int64
	var totalBloatRemoved int64
	var limitReached int64 // 0 = false, 1 = true

	// Create worker connections
	workers := make([]*db.DB, numWorkers)
	for i := 0; i < numWorkers; i++ {
		worker, err := connection.NewWorkerConnection(i + 1)
		if err != nil {
			fatal("failed to create worker connection", "worker", i+1, "error", err)
		}
		workers[i] = worker
	}
	defer func() {
		for _, w := range workers {
			w.Close()
		}
	}()

	// Start progress display goroutine
	stopProgress := make(chan struct{})
	if !jsonFlag && !verboseFlag && !dryRunFlag {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					completed := atomic.LoadInt64(&tablesCompleted)
					printParallelProgress(int(completed), len(tables), numWorkers)
				case <-stopProgress:
					return
				}
			}
		}()
	}

	// Start workers
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Add(1)
		go func(w *db.DB) {
			defer wg.Done()
			for table := range tableChan {
				// Check if limit reached before processing
				if limitBytes > 0 && atomic.LoadInt64(&totalBloatRemoved) >= limitBytes {
					atomic.StoreInt64(&limitReached, 1)
					// Still need to report skipped tables
					resultChan <- analysis.DebloatResult{
						Table: table,
						Error: "skipped (limit reached)",
					}
					continue
				}

				result := processTable(w, table)
				resultChan <- result

				// Update counters
				atomic.AddInt64(&tablesCompleted, 1)
				if result.BloatRemoved > 0 {
					atomic.AddInt64(&totalBloatRemoved, int64(result.BloatRemoved)*8192)
				}
			}
		}(worker)
	}

	// Send tables to workers
	for _, table := range tables {
		tableChan <- table
	}
	close(tableChan)

	// Wait for all workers to finish
	wg.Wait()
	close(resultChan)

	// Stop progress display
	close(stopProgress)
	if !jsonFlag && !verboseFlag && !dryRunFlag {
		fmt.Print("\r\033[K") // Clear progress line
	}

	// Collect results (maintain order by table name for consistent output)
	resultMap := make(map[string]analysis.DebloatResult)
	for result := range resultChan {
		resultMap[result.Table] = result
	}

	// Return results in original table order
	results := make([]analysis.DebloatResult, 0, len(tables))
	for _, table := range tables {
		if result, ok := resultMap[table]; ok {
			results = append(results, result)
		}
	}

	return results, atomic.LoadInt64(&limitReached) == 1
}

// printParallelProgress prints progress for parallel mode
func printParallelProgress(completed, total, workers int) {
	// Calculate progress bar
	barWidth := 30
	progress := float64(completed) / float64(total)
	filled := int(progress * float64(barWidth))

	bar := strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled)
	workerWord := "workers"
	if workers == 1 {
		workerWord = "worker"
	}
	fmt.Printf("\r\033[K[%s] %d/%d tables | %d %s", bar, completed, total, workers, workerWord)
}

// getTargetTables returns the list of tables to debloat based on flags
func getTargetTables(connection *db.DB) ([]string, error) {
	// If specific tables are provided, use them
	if len(targetTables) > 0 {
		return targetTables, nil
	}

	// Otherwise, get all tables (filtered by schema if specified)
	tables, err := connection.ListTablesFiltered(targetSchemas, systemFlag, excludeTbl)
	if err != nil {
		return nil, err
	}

	return tables, nil
}

// processTable debloats a single table and returns the result
func processTable(connection *db.DB, table string) analysis.DebloatResult {
	startTime := time.Now()
	result := analysis.DebloatResult{Table: table, DryRun: dryRunFlag}

	// Get initial bloat estimate
	bloatPages, err := connection.GetBloatPages(table)
	if err != nil {
		result.Error = fmt.Sprintf("failed to estimate bloat: %v", err)
		return result
	}

	if bloatPages <= 0 {
		if verboseFlag {
			fmt.Printf("  %s: no bloat detected, skipping\n", table)
		}
		return result
	}

	// Get initial page count
	initialPages, err := connection.GetTablePages(table)
	if err != nil {
		result.Error = fmt.Sprintf("failed to get page count: %v", err)
		return result
	}
	result.InitialPages = initialPages

	if dryRunFlag {
		// Dry-run: just show what would happen
		if verboseFlag {
			fmt.Printf("  %s: would compact %d bloat pages (current: %d pages)\n",
				table, bloatPages, initialPages)
			if reindexFlag {
				fmt.Printf("  %s: would reindex after compaction\n", table)
			}
		}
		result.FinalPages = initialPages - bloatPages // estimated
		result.BloatRemoved = bloatPages
		return result
	}

	// Actually compact the table
	if verboseFlag {
		fmt.Printf("  %s: compacting %d pages...\n", table, bloatPages)
	}
	var compactErr error
	// Number of passes depends on mode
	passes := 2 // default: 2 passes
	if fastFlag {
		passes = 1 // fast: 1 pass only
	} else if slowFlag {
		passes = 3 // slow: 3 passes for thorough compaction
	}
	for pass := 1; pass <= passes; pass++ {
		compactErr = connection.CompactTableUpdate(table)
		if compactErr != nil {
			break
		}
	}

	if compactErr != nil {
		result.Error = fmt.Sprintf("compaction failed: %v", compactErr)
		return result
	}

	// Get final page count
	finalPages, _ := connection.GetTablePages(table)
	result.FinalPages = finalPages
	result.BloatRemoved = initialPages - finalPages
	result.BloatRemovedBytes = int64(result.BloatRemoved) * 8192

	// Reindex if requested
	if reindexFlag {
		if verboseFlag {
			fmt.Printf("  %s: reindexing...\n", table)
		}
		if err := connection.ReindexTable(table); err != nil {
			result.Error = fmt.Sprintf("reindex failed: %v", err)
		} else {
			result.Reindexed = true
		}
	}

	result.DurationMs = time.Since(startTime).Milliseconds()
	return result
}

// parseLimit parses the --limit flag value and returns either:
// - bytes limit (> 0) for size-based limits (e.g., "500MB", "1GB")
// - percentage (0.0-100.0) for percentage-based limits (e.g., "50%")
// Returns (bytes, percentage, error)
func parseLimit(limitStr string) (int64, float64, error) {
	if limitStr == "" {
		return 0, 0, nil
	}

	limitStr = strings.TrimSpace(limitStr)

	// Check for percentage
	if strings.HasSuffix(limitStr, "%") {
		percentStr := strings.TrimSuffix(limitStr, "%")
		percent, err := strconv.ParseFloat(percentStr, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid percentage value: %s", limitStr)
		}
		if percent <= 0 || percent > 100 {
			return 0, 0, fmt.Errorf("percentage must be between 0 and 100, got %.2f%%", percent)
		}
		return 0, percent, nil
	}

	// Parse size (e.g., 500MB, 1GB, 2.5GB)
	var multiplier int64 = 1
	var valueStr string

	limitUpper := strings.ToUpper(limitStr)
	if strings.HasSuffix(limitUpper, "GB") {
		multiplier = 1024 * 1024 * 1024
		valueStr = limitStr[:len(limitStr)-2]
	} else if strings.HasSuffix(limitUpper, "MB") {
		multiplier = 1024 * 1024
		valueStr = limitStr[:len(limitStr)-2]
	} else if strings.HasSuffix(limitUpper, "KB") {
		multiplier = 1024
		valueStr = limitStr[:len(limitStr)-2]
	} else if strings.HasSuffix(limitUpper, "B") {
		multiplier = 1
		valueStr = limitStr[:len(limitStr)-1]
	} else {
		return 0, 0, fmt.Errorf("invalid limit format: %s (use: 500MB, 1GB, 50%%)", limitStr)
	}

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid size value: %s", limitStr)
	}
	if value <= 0 {
		return 0, 0, fmt.Errorf("limit size must be positive, got %s", limitStr)
	}

	bytes := int64(value * float64(multiplier))
	return bytes, 0, nil
}
