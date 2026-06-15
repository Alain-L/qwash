package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Alain-L/qwash/analysis"
	"github.com/Alain-L/qwash/db"
	"github.com/Alain-L/qwash/output"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Global Flags
var (
	// Database connection. Empty values are omitted from the connection
	// string so that pgx falls back to the standard PostgreSQL client
	// conventions (PGHOST, PGUSER, PGPASSWORD, ~/.pgpass, defaults...).
	dbName         string // --dbname (-d)
	user           string // --dbuser (-U)
	host           string // --host (-h)
	port           string // --port (-p)
	passwordPrompt bool   // --password (-W): force interactive password prompt
	sslMode        string // --sslmode
	testConnFlag   bool   // --test-connection

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

// exitCode is the process exit status, set by the run functions:
//
//	0 = success
//	1 = fatal error (set via fatal(); e.g. bad flags, connection failure)
//	2 = completed with per-table errors (some tables could not be processed)
//
// It is applied in Execute() after the command returns, so that deferred
// cleanup (connection close) still runs before the process exits.
var exitCode int

// Execute runs the CLI
func Execute(version, commit, date string) {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)

	initLogger()

	if err := rootCmd.Execute(); err != nil {
		fatal("command failed", "error", err)
	}
	os.Exit(exitCode)
}

// init sets up the CLI flags
func init() {
	// Define the help flag without a shorthand (like psql, which uses -? and
	// --help) so that -h stays available for --host. Cobra only adds its own
	// -h shorthand when no help flag is registered.
	rootCmd.PersistentFlags().Bool("help", false,
		"Show help")

	// Database connection options, following the usual PostgreSQL client
	// conventions: same short flags as psql, PG* environment variables and
	// ~/.pgpass honored when a parameter is not given.
	rootCmd.PersistentFlags().StringVarP(&dbName, "dbname", "d", "",
		"Database name (default: PGDATABASE, or the user name)")
	rootCmd.PersistentFlags().StringVarP(&user, "dbuser", "U", "",
		"Database user (default: PGUSER, or the OS user)")
	rootCmd.PersistentFlags().StringVarP(&host, "host", "h", "",
		"Database host or socket directory (default: PGHOST, or local socket)")
	rootCmd.PersistentFlags().StringVarP(&port, "port", "p", "",
		"Database port (default: PGPORT, or 5432)")
	rootCmd.PersistentFlags().BoolVarP(&passwordPrompt, "password", "W", false,
		"Force password prompt (default: PGPASSWORD, or ~/.pgpass)")
	rootCmd.PersistentFlags().StringVar(&sslMode, "sslmode", "",
		"SSL mode: disable, allow, prefer, require, verify-ca, verify-full (default: PGSSLMODE, or prefer)")

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
		"Show detailed bloat analysis per table and index (not yet implemented)")
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

	// Build connection config. Only explicitly-given parameters are set;
	// pgx resolves the rest from the PG* environment and libpq defaults.
	dbConfig := db.Config{
		Host:     host,
		Port:     port,
		User:     user,
		Database: dbName,
		SSLMode:  sslMode,
	}
	if passwordPrompt {
		dbConfig.Password = promptPassword()
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
		connection, err := db.Connect(dbConfig, verboseFlag)
		if err != nil {
			fatal("connection failed", "error", err)
		}
		defer connection.Close()

		// Show the target actually resolved by pgx (flags, PG* environment
		// variables or defaults), so the user can see where they landed.
		fmt.Printf("Connection OK (%s)\n", connection.ResolvedTarget())
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

	// Cancel long operations cleanly on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Step 4: Determine operation mode
	switch {
	case estimateFlag:
		runEstimate(ctx, connection)
		return

	case debloatFlag:
		runDebloat(ctx, connection)
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

// keepIf returns the items of a slice matching the predicate.
func keepIf[T any](items []T, pred func(T) bool) []T {
	var out []T
	for _, it := range items {
		if pred(it) {
			out = append(out, it)
		}
	}
	return out
}

// promptPassword interactively reads the database password (--password/-W),
// without echoing it to the terminal — same behavior as psql -W.
func promptPassword() string {
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fatal("failed to read password", "error", err)
	}
	return string(pw)
}

// runEstimate executes the bloat estimation report
func runEstimate(ctx context.Context, connection *db.DB) {
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
		tableBloat, err = analysis.DetectTableBloat(ctx, connection)
		if err != nil {
			fatal("failed to analyze table bloat", "error", err)
		}
	}

	// Analyze TOAST bloat if --toast is enabled
	var toastBloat []analysis.ToastBloat
	if toastFlag {
		toastBloat, err = analysis.DetectToastBloat(ctx, connection)
		if err != nil {
			slog.Warn("failed to analyze TOAST bloat", "error", err)
			// Continue without TOAST data
		}
	}

	// Analyze B-Tree index bloat if --btree is enabled
	var indexBloat []analysis.BloatIndex
	if btreeFlag {
		indexBloat, err = analysis.DetectBtreeIndexBloat(ctx, connection)
		if err != nil {
			slog.Warn("failed to analyze B-Tree index bloat", "error", err)
			// Continue without index data
		}
	}

	// Hide system-schema objects unless --system is given. The embedded
	// queries now return system catalogs too (so --system can surface them),
	// and this is the single authoritative place that filters them out.
	if !systemFlag {
		isSystem := func(schema string) bool {
			switch schema {
			case "pg_catalog", "information_schema", "pg_toast":
				return true
			}
			return false
		}
		tableBloat = keepIf(tableBloat, func(t analysis.BloatTable) bool { return !isSystem(t.Schema) })
		toastBloat = keepIf(toastBloat, func(t analysis.ToastBloat) bool { return !isSystem(t.Schema) })
		indexBloat = keepIf(indexBloat, func(i analysis.BloatIndex) bool { return !isSystem(i.Schema) })
	}

	// Apply --schema and --exclude-table filters. These used to be silently
	// ignored in estimate mode (only --debloat honored them), so a report
	// could include tables the user believed were filtered out.
	if len(targetSchemas) > 0 {
		tableBloat = keepIf(tableBloat, func(t analysis.BloatTable) bool { return slices.Contains(targetSchemas, t.Schema) })
		toastBloat = keepIf(toastBloat, func(t analysis.ToastBloat) bool { return slices.Contains(targetSchemas, t.Schema) })
		indexBloat = keepIf(indexBloat, func(i analysis.BloatIndex) bool { return slices.Contains(targetSchemas, i.Schema) })
	}
	if len(excludeTbl) > 0 {
		excluded := func(schema, table string) bool {
			for _, x := range excludeTbl {
				if x == table || x == schema+"."+table {
					return true
				}
			}
			return false
		}
		tableBloat = keepIf(tableBloat, func(t analysis.BloatTable) bool { return !excluded(t.Schema, t.TableName) })
		toastBloat = keepIf(toastBloat, func(t analysis.ToastBloat) bool { return !excluded(t.Schema, t.TableName) })
		indexBloat = keepIf(indexBloat, func(i analysis.BloatIndex) bool { return !excluded(i.Schema, i.TableName) })
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
func runDebloat(ctx context.Context, connection *db.DB) {
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

	// Estimate bloat for the whole database in a single catalog-wide scan,
	// then look results up per table. Running the bloat query once (instead of
	// once per table, and again per compaction pass) avoids quadratic cost on
	// large databases.
	allBloat, err := connection.GetAllBloatPages()
	if err != nil {
		fatal("failed to estimate bloat", "error", err)
	}

	// Calculate total bloat, total size, and build map for LPT scheduling
	// LPT (Longest Processing Time First) sorts tables by bloat size descending
	// to optimize parallel processing - biggest tables first so smaller ones fill gaps
	var totalBloat int64
	var totalDatabaseSize int64
	tableBloatPages := make(map[string]int)
	for _, table := range tables {
		if bloatPages := allBloat[table]; bloatPages > 0 {
			totalBloat += int64(bloatPages) * 8192 // Convert pages to bytes (8KB per page)
			tableBloatPages[table] = bloatPages
		}
		// Get total table size for percentage calculation (cheap pg_class lookup)
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
		results, limitReached = runDebloatSequential(ctx, connection, tables, tableBloatPages, limitBytes)
	} else {
		// Parallel mode
		results, limitReached = runDebloatParallel(ctx, connection, tables, tableBloatPages, numWorkers, limitBytes)
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

	// Signal per-table failures through the exit code (for automation).
	// Skipped tables (limit reached) carry no error and don't count.
	for _, r := range results {
		if r.Error != "" {
			exitCode = 2
			break
		}
	}
}

// runDebloatSequential processes tables one at a time (original behavior)
func runDebloatSequential(ctx context.Context, connection *db.DB, tables []string, bloatByTable map[string]int, limitBytes int64) ([]analysis.DebloatResult, bool) {
	var results []analysis.DebloatResult
	var totalBloatRemoved int64
	limitReached := false

	// Set total tables for progress display
	connection.TotalTables = len(tables)
	// Silence internal progress when showing main progress bar (non-verbose, non-json)
	connection.SilentProgress = jsonFlag || (!verboseFlag && !dryRunFlag)

	for i, table := range tables {
		// Stop launching new work on Ctrl-C; tables already done are kept.
		if ctx.Err() != nil {
			slog.Warn("interrupted; stopping before remaining tables", "processed", i, "total", len(tables))
			break
		}

		// Check if limit is reached: record the remaining tables as skipped
		// (not processed) so the report and counts match parallel mode.
		if limitBytes > 0 && totalBloatRemoved >= limitBytes {
			limitReached = true
			for _, skip := range tables[i:] {
				results = append(results, analysis.DebloatResult{Table: skip, Skipped: true})
			}
			break
		}

		// Show progress bar (same as parallel mode)
		if !jsonFlag && !verboseFlag && !dryRunFlag {
			printParallelProgress(i, len(tables), 1)
		}

		connection.CurrentTableIndex = i
		result := processTable(ctx, connection, table, bloatByTable[table])
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
func runDebloatParallel(ctx context.Context, connection *db.DB, tables []string, bloatByTable map[string]int, numWorkers int, limitBytes int64) ([]analysis.DebloatResult, bool) {
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
				// Stop processing new tables on Ctrl-C; drain the channel
				// without working so the dispatcher's sends don't block.
				if ctx.Err() != nil {
					continue
				}

				// Check if limit reached before processing
				if limitBytes > 0 && atomic.LoadInt64(&totalBloatRemoved) >= limitBytes {
					atomic.StoreInt64(&limitReached, 1)
					// Report the table as skipped (an expected outcome, not an
					// error), and count it so the progress bar still reaches 100%.
					resultChan <- analysis.DebloatResult{Table: table, Skipped: true}
					atomic.AddInt64(&tablesCompleted, 1)
					continue
				}

				result := processTable(ctx, w, table, bloatByTable[table])
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

	if ctx.Err() != nil {
		slog.Warn("interrupted; stopped processing remaining tables")
	}

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
	// If specific tables are provided, resolve each one to its canonical
	// schema-qualified name (search_path rules) so that every downstream
	// consumer — estimation, advisory lock, DML — designates the same
	// relation. This also fails fast on tables that don't exist, instead
	// of reporting a confusing per-table error later.
	if len(targetTables) > 0 {
		resolved := make([]string, 0, len(targetTables))
		for _, t := range targetTables {
			qualified, err := connection.ResolveTableName(t)
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, qualified)
		}
		return resolved, nil
	}

	// Otherwise, get all tables (filtered by schema if specified);
	// ListTablesFiltered already returns schema-qualified names.
	tables, err := connection.ListTablesFiltered(targetSchemas, systemFlag, excludeTbl)
	if err != nil {
		return nil, err
	}

	return tables, nil
}

// processTable debloats a single table and returns the result.
// bloatPages is the estimated bloat (in pages) precomputed once for the whole
// database, so this function does not re-run the catalog-wide bloat query.
func processTable(ctx context.Context, connection *db.DB, table string, bloatPages int) analysis.DebloatResult {
	startTime := time.Now()
	result := analysis.DebloatResult{Table: table, DryRun: dryRunFlag}

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

	// Target page count: compacting down to (current pages - bloat pages).
	// Computed once and held fixed across passes, so later passes don't chase
	// a target that drifts below the real minimum as the table shrinks.
	toPage := initialPages - bloatPages
	if toPage < 0 {
		toPage = 0
	}

	// Preflight safety checks: surface advisories and refuse unsafe tables
	// (non-owner -> VACUUM can't reclaim; no usable REPLICA IDENTITY -> UPDATE
	// would fail). In dry-run we report the blocking reason but keep going so
	// the estimate is still shown.
	warnings, preflightErr := connection.DebloatPreflight(table)
	for _, w := range warnings {
		slog.Warn(w)
	}
	if preflightErr != nil {
		if dryRunFlag {
			slog.Warn("debloat would be refused", "table", table, "reason", preflightErr)
		} else {
			result.Error = preflightErr.Error()
			return result
		}
	}

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
		compactErr = connection.CompactTableToTarget(ctx, table, toPage)
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
		if err := connection.ReindexTable(ctx, table); err != nil {
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
