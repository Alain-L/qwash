package analysis

// DebloatResult holds the result of debloating a single table
type DebloatResult struct {
	Table             string `json:"table"`
	InitialPages      int    `json:"initial_pages"`
	FinalPages        int    `json:"final_pages"`
	BloatRemoved      int    `json:"bloat_removed_pages"`
	BloatRemovedBytes int64  `json:"bloat_removed_bytes"`
	DurationMs        int64  `json:"duration_ms"`
	Reindexed         bool   `json:"reindexed,omitempty"`
	Error             string `json:"error,omitempty"`
	DryRun            bool   `json:"dry_run,omitempty"`
}

// BloatIndex represents bloat information for an index.
type BloatIndex struct {
	Schema     string  `json:"schema"`
	IndexName  string  `json:"index_name"`
	TableName  string  `json:"table_name"`
	IndexSize  int64   `json:"index_size"`
	BloatSize  int64   `json:"bloat_size"`
	BloatRatio float64 `json:"bloat_ratio"`
}

// BloatTable represents bloat information for a table.
type BloatTable struct {
	Schema     string  `json:"schema"`
	TableName  string  `json:"table_name"`
	TableSize  int64   `json:"table_size"`
	BloatSize  int64   `json:"bloat_size"`
	BloatRatio float64 `json:"bloat_ratio"`
	// Detailed fields for -t --estimate mode
	Pages      int   `json:"pages"`
	MinPages   int   `json:"min_pages"`
	LiveTuples int64 `json:"live_tuples"`
	DeadTuples int64 `json:"dead_tuples"`
	FillFactor int   `json:"fill_factor"`
}

// ToastBloat represents bloat information for a TOAST table.
// Note: Bloat estimation is only reliable for TOAST tables >= 10 MB.
type ToastBloat struct {
	Schema      string   `json:"schema"`
	TableName   string   `json:"table_name"`
	ToastSize   int64    `json:"toast_size"`
	ToastPages  int      `json:"toast_pages"`
	ToastChunks int64    `json:"toast_chunks"`
	BloatPct    *float64 `json:"bloat_pct,omitempty"`  // nil if < 10 MB or no ppc_ref
	BloatSize   int64    `json:"bloat_size,omitempty"` // 0 if unreliable
	Warning     string   `json:"warning,omitempty"`    // "< 10 MB" or "no chunks"
}
