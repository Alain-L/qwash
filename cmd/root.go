package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"qwash/analysis"
	"qwash/db"
	"qwash/output"
	"time"

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

	// Debloat options
	debloatFlag bool // --debloat (-B)
	fastFlag    bool // --fast
	slowFlag    bool // --slow (1 page at a time with delay, like pgcompacttable)
	delayMs     int  // --delay (milliseconds between operations in slow mode)
	dryRunFlag  bool // --dry-run
	reindexFlag bool // --reindex

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
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error: %v", err)
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

	// Debloat options
	rootCmd.PersistentFlags().BoolVarP(&debloatFlag, "debloat", "B", false,
		"Perform bloat reduction on tables")
	rootCmd.PersistentFlags().BoolVar(&fastFlag, "fast", false,
		"Fast mode: ~97%% efficiency, significantly faster")
	rootCmd.PersistentFlags().BoolVar(&slowFlag, "slow", false,
		"Slow mode: 1 page at a time with delay (like pgcompacttable)")
	rootCmd.PersistentFlags().IntVar(&delayMs, "delay", 10,
		"Delay in milliseconds between operations in slow mode (default: 10)")
	rootCmd.PersistentFlags().BoolVar(&dryRunFlag, "dry-run", false,
		"Show what would be done without making changes")
	rootCmd.PersistentFlags().BoolVar(&reindexFlag, "reindex", false,
		"Rebuild indexes after debloat (REINDEX CONCURRENTLY)")

	// Output options
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false,
		"Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&jsonFlag, "json", "J", false,
		"Output results in JSON format")
}

// executeAnalysis is the core function that orchestrates the pipeline
func executeAnalysis(cmd *cobra.Command, args []string) {
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
		log.Fatalf("[ERROR] --estimate (-E) and --debloat (-B) cannot be used together.")
	}

	// --reindex requires --debloat
	if reindexFlag && !debloatFlag {
		log.Fatalf("[ERROR] --reindex requires --debloat (-B).")
	}

	// --fast, --slow, --dry-run require --debloat
	if (fastFlag || slowFlag || dryRunFlag) && !debloatFlag {
		log.Fatalf("[ERROR] --fast, --slow, and --dry-run require --debloat (-B).")
	}

	// --fast and --slow are mutually exclusive
	if fastFlag && slowFlag {
		log.Fatalf("[ERROR] --fast and --slow cannot be used together.")
	}

	// --delay requires --slow
	if cmd.Flags().Changed("delay") && !slowFlag {
		log.Fatalf("[ERROR] --delay can only be used with --slow mode.")
	}

	// Validate --delay value
	if delayMs < 0 {
		log.Fatalf("[ERROR] --delay must be >= 0 (got %d).", delayMs)
	}
	if delayMs > 10000 {
		log.Fatalf("[ERROR] --delay must be <= 10000ms (got %d). Use lower values for reasonable performance.", delayMs)
	}

	// Require at least one mode
	if !estimateFlag && !debloatFlag && !testConnFlag && !detailFlag {
		// Default to --estimate if no mode specified
		estimateFlag = true
	}

	// Step 2: Test database connection if requested
	if testConnFlag {
		if verboseFlag {
			fmt.Printf("Connecting to %s@%s:%s/%s...\n",
				dbConfig.User, dbConfig.Host, dbConfig.Port, dbConfig.Database)
		}
		connection, err := db.Connect(dbConfig, verboseFlag)
		if err != nil {
			log.Fatalf("Connection failed: %v", err)
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
		log.Fatalf("Failed to connect: %v", err)
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
	}

	// Analyze table bloat
	tableBloat, err := analysis.DetectTableBloat(context.Background(), connection)
	if err != nil {
		log.Fatalf("Failed to analyze table bloat: %v", err)
	}

	// Display results based on output format
	if jsonFlag {
		output.PrintBloatJSON(tableBloat, nil)
	} else {
		output.PrintBloatSummary(tableBloat, nil)
	}
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

	// Show mode
	if dryRunFlag {
		fmt.Println("DRY-RUN MODE: No changes will be made")
	}
	if fastFlag {
		fmt.Println("FAST MODE: Using adaptive vacuum (~99% efficiency)")
	}

	// Get list of tables to process
	tables, err := getTargetTables(connection)
	if err != nil {
		log.Fatalf("Failed to get target tables: %v", err)
	}

	if len(tables) == 0 {
		fmt.Println("No tables to debloat.")
		return
	}

	if verboseFlag || dryRunFlag {
		fmt.Printf("Tables to process: %d\n", len(tables))
		for _, t := range tables {
			fmt.Printf("  - %s\n", t)
		}
	}

	// Process each table
	var results []debloatResult
	for _, table := range tables {
		result := processTable(connection, table)
		results = append(results, result)
	}

	// Output results
	if jsonFlag {
		printDebloatJSON(results)
	} else {
		printDebloatSummary(results)
	}
}

// debloatResult holds the result of debloating a single table
type debloatResult struct {
	Table        string        `json:"table"`
	InitialPages int           `json:"initial_pages"`
	FinalPages   int           `json:"final_pages"`
	BloatRemoved int           `json:"bloat_removed"`
	Duration     time.Duration `json:"duration_ms"`
	Reindexed    bool          `json:"reindexed,omitempty"`
	Error        string        `json:"error,omitempty"`
	DryRun       bool          `json:"dry_run,omitempty"`
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
func processTable(connection *db.DB, table string) debloatResult {
	startTime := time.Now()
	result := debloatResult{Table: table, DryRun: dryRunFlag}

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
		fmt.Printf("  %s: would compact %d bloat pages (current: %d pages)\n",
			table, bloatPages, initialPages)
		if reindexFlag {
			fmt.Printf("  %s: would reindex after compaction\n", table)
		}
		result.FinalPages = initialPages - bloatPages // estimated
		result.BloatRemoved = bloatPages
		return result
	}

	// Actually compact the table
	fmt.Printf("  %s: compacting...\n", table)
	var compactErr error
	if fastFlag {
		compactErr = connection.CompactTableFast(table, bloatPages)
	} else if slowFlag {
		compactErr = connection.CompactTableSlow(table, bloatPages, delayMs)
	} else {
		compactErr = connection.CompactTable(table, bloatPages)
	}

	if compactErr != nil {
		result.Error = fmt.Sprintf("compaction failed: %v", compactErr)
		return result
	}

	// Get final page count
	finalPages, _ := connection.GetTablePages(table)
	result.FinalPages = finalPages
	result.BloatRemoved = initialPages - finalPages

	// Reindex if requested
	if reindexFlag {
		fmt.Printf("  %s: reindexing...\n", table)
		if err := connection.ReindexTable(table); err != nil {
			result.Error = fmt.Sprintf("reindex failed: %v", err)
		} else {
			result.Reindexed = true
		}
	}

	result.Duration = time.Since(startTime)
	return result
}

// printDebloatSummary prints a text summary of debloat results
func printDebloatSummary(results []debloatResult) {
	fmt.Println()
	fmt.Println("Debloat Summary:")
	fmt.Println("================")

	var totalRemoved int
	var totalDuration time.Duration
	var errors int

	for _, r := range results {
		totalDuration += r.Duration
		if r.Error != "" {
			fmt.Printf("  %s: ERROR - %s\n", r.Table, r.Error)
			errors++
		} else if r.BloatRemoved > 0 {
			fmt.Printf("  %s: %d pages removed (%d -> %d) in %s\n",
				r.Table, r.BloatRemoved, r.InitialPages, r.FinalPages, r.Duration.Round(time.Millisecond))
			totalRemoved += r.BloatRemoved
		}
	}

	fmt.Println()
	fmt.Printf("Total: %d pages removed from %d tables in %s", totalRemoved, len(results)-errors, totalDuration.Round(time.Millisecond))
	if errors > 0 {
		fmt.Printf(" (%d errors)", errors)
	}
	fmt.Println()
}

// printDebloatJSON prints debloat results in JSON format
func printDebloatJSON(results []debloatResult) {
	// Use the output package for consistent JSON formatting
	data := struct {
		Results      []debloatResult `json:"results"`
		TotalRemoved int             `json:"total_pages_removed"`
		TablesCount  int             `json:"tables_processed"`
	}{
		Results:     results,
		TablesCount: len(results),
	}

	for _, r := range results {
		data.TotalRemoved += r.BloatRemoved
	}

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal JSON: %v", err)
	}
	fmt.Println(string(jsonBytes))
}
