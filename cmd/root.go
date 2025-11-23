package cmd

import (
	"context"
	"fmt"
	"log"
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
	sslMode      string   // --sslMode
	testConnFlag bool     // --test-connection

	// Analysis options
	estimateFlag bool     // --estimate
	detailFlag   bool     // --detail
	debloatFlag  bool     // --debloat
	excludeTbl   []string // --exclude-table
	targetTables []string // --table (-t)

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
	rootCmd.PersistentFlags().StringVarP(&sslMode, "sslmode", "S", "disable",
		"SSL mode (disable, require, verify-ca, verify-full)")

	// Database testing
	rootCmd.PersistentFlags().BoolVarP(&testConnFlag, "test-connection", "T", false,
		"Test the database connection and exit")

	// Analysis options
	rootCmd.PersistentFlags().BoolVarP(&estimateFlag, "estimate", "E", false,
		"Display a report of estimated bloat (default)")
	rootCmd.PersistentFlags().BoolVarP(&detailFlag, "detail", "D", false,
		"Show detailed bloat analysis per table and index")
	rootCmd.PersistentFlags().BoolVarP(&debloatFlag, "debloat", "B", false,
		"Perform bloat reduction on identified tables (cautiously)")
	rootCmd.PersistentFlags().StringSliceVarP(&excludeTbl, "exclude-table", "X", nil,
		"Exclude specific tables from analysis and bloat removal")
	rootCmd.PersistentFlags().StringSliceVarP(&targetTables, "table", "t", nil,
		"Specify table(s) to debloat (can be used multiple times)")

	// Output options
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false,
		"Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&jsonFlag, "json", "J", false,
		"Output results in JSON format")
}

// executeAnalysis is the core function that orchestrates the pipeline
func executeAnalysis(cmd *cobra.Command, args []string) {
	fmt.Println("[INFO] Starting qwash analysis...")

	// Build connection config
	dbConfig := db.Config{
		Host:     getFirstOrDefault(host, "localhost"),
		Port:     getFirstOrDefault(port, "5432"),
		User:     getFirstOrDefault(user, "postgres"),
		Password: pass,
		Database: getFirstOrDefault(dbName, "postgres"),
		SSLMode:  sslMode,
	}

	// Step 1: Validate conflicting flags
	if estimateFlag && debloatFlag {
		log.Fatalf("[ERROR] --estimate (-E) and --debloat (-B) cannot be used together.")
	}

	// Step 2: Display connection settings (if provided)
	if len(dbName) > 0 {
		fmt.Printf("[INFO] Target databases: %v\n", dbName)
	} else {
		fmt.Println("[INFO] No specific database provided, using default connection settings.")
	}

	if len(user) > 0 {
		fmt.Printf("[INFO] Connecting as user: %v\n", user)
	}

	if len(host) > 0 {
		fmt.Printf("[INFO] Database host: %v\n", host)
	} else {
		fmt.Println("[INFO] Using default host: localhost")
	}

	if len(port) > 0 {
		fmt.Printf("[INFO] Database port: %v\n", port)
	} else {
		fmt.Println("[INFO] Using default port: 5432")
	}

	// Step 3: Test database connection if requested
	if testConnFlag {
		fmt.Println("[INFO] Testing database connection...")
		connection, err := db.Connect(dbConfig)
		if err != nil {
			fmt.Println("[ERROR] Connection test failed:", err)
			return
		}
		defer connection.Close()

		fmt.Println("[SUCCESS] Connection test passed!")

		// Retrieve available databases
		databases, err1 := connection.ListDatabases()
		if err1 != nil {
			fmt.Println("[ERROR] Failed to retrieve databases:", err1)
		} else {
			fmt.Println("[INFO] Available databases:")
			for _, db := range databases {
				fmt.Printf("    - %s\n", db)
			}
		}
		return
	}

	// Step 4: Establish database connection
	connection, err := db.Connect(dbConfig)
	if err != nil {
		log.Fatalf("[ERROR] Failed to connect to database: %v", err)
	}
	defer connection.Close()

	if len(excludeTbl) > 0 {
		fmt.Printf("[INFO] Excluding tables from analysis: %v\n", excludeTbl)
	}

	// Step 5: Determine operation mode
	switch {
	case estimateFlag:
		fmt.Println("[INFO] Running bloat estimation...")

		// Analyze table bloat
		tableBloat, err := analysis.DetectTableBloat(context.Background(), connection)
		if err != nil {
			log.Fatalf("[ERROR] Failed to analyze table bloat: %v", err)
		}

		// Display results based on output format
		if jsonFlag {
			output.PrintBloatJSON(tableBloat, nil)
		} else {
			output.PrintBloatSummary(tableBloat, nil)
		}
		return

	case debloatFlag:
		fmt.Println("[WARNING] Bloat reduction is an experimental feature. Use with caution.")
		fmt.Println("[INFO] Running bloat reduction process...")

		if len(targetTables) == 0 {
			fmt.Println("[ERROR] No target tables specified. Use -t to define tables.")
			return
		}

		for _, table := range targetTables {
			fmt.Printf("[INFO] Compacting table: %s\n", table)
			bloatPages, err := connection.GetBloatPages(table)
			if err != nil {
				fmt.Printf("[ERROR] Failed to estimate bloat on %s: %v\n", table, err)
				continue
			}
			if err := connection.CompactTable(table, bloatPages); err != nil {
				fmt.Printf("[ERROR] Failed to debloat table %s: %v\n", table, err)
			} else {
				fmt.Printf("[INFO] Debloat completed for table: %s\n", table)
			}
		}
		return

	case detailFlag:
		fmt.Println("[INFO] Displaying detailed bloat analysis per table and index...")
		fmt.Println("[TODO] Show detailed per-table bloat statistics")
		return

	default:
		fmt.Println("[INFO] Displaying global bloat summary...")
		fmt.Println("[TODO] Compute and display summary statistics on bloat")
	}
}

// Helper function to get the first value from a slice or return a default value
func getFirstOrDefault(arr []string, defaultValue string) string {
	if len(arr) > 0 {
		return arr[0]
	}
	return defaultValue
}
